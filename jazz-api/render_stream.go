package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

type renderUpload interface {
	io.Writer
	Close() error
	CloseWithError(error) error
}

// Each stream is drained concurrently with bounded OS/storage buffers. A failed
// upload cancels FFmpeg and every other upload; no completed pair is buffered.
func runRenderPipeline(parent context.Context, args []string, count int, open func(context.Context, int) renderUpload, sources []renderSource) error {
	if count < 1 || count > 2 {
		return errors.New("invalid render output count")
	}
	runCtx, cancel := context.WithCancel(parent)
	defer cancel()
	group, ctx := errgroup.WithContext(runCtx)
	command := exec.CommandContext(ctx, envOr("FFMPEG_PATH", "ffmpeg"), args...)
	var stderr cappedBuffer
	command.Stderr = &stderr // Never expose signed source URLs from FFmpeg errors.
	readers, writers := make([]*os.File, count), make([]*os.File, count)
	var rawReaders, rawWriters []*os.File
	uploads := make([]renderUpload, count)
	defer func() {
		for _, r := range readers {
			if r != nil {
				_ = r.Close()
			}
		}
		for _, w := range writers {
			if w != nil {
				_ = w.Close()
			}
		}
		for _, file := range append(rawReaders, rawWriters...) {
			_ = file.Close()
		}
	}()
	for index := range readers {
		var err error
		readers[index], writers[index], err = os.Pipe()
		if err != nil {
			return errors.New("render pipe unavailable")
		}
	}
	command.Stdout = writers[0]
	if count > 1 {
		command.ExtraFiles = writers[1:]
	}
	if len(sources) > 0 {
		if count != 2 {
			return errors.New("paired encoder requires two outputs")
		}
		var manifest strings.Builder
		manifest.WriteString("ffconcat version 1.0\n")
		for index, source := range sources {
			reader, writer, err := os.Pipe()
			if err != nil {
				return errors.New("render input pipe unavailable")
			}
			rawReaders = append(rawReaders, reader)
			rawWriters = append(rawWriters, writer)
			fmt.Fprintf(&manifest, "file 'pipe:%d'\nduration %.9f\n", 4+index, float64(renderFrameCount(source))/renderFrameRate)
		}
		command.ExtraFiles = append(command.ExtraFiles, rawReaders...)
		command.Stdin = strings.NewReader(manifest.String())
	}
	for index := range uploads {
		uploads[index] = open(ctx, index)
	}
	if err := command.Start(); err != nil {
		for _, upload := range uploads {
			_ = upload.CloseWithError(err)
		}
		return errors.New("render encoder unavailable")
	}
	for _, writer := range writers {
		_ = writer.Close()
	} // Only child owns write ends.
	for _, reader := range rawReaders {
		_ = reader.Close()
	}
	for index := range readers {
		group.Go(func() error {
			if _, err := io.Copy(uploads[index], readers[index]); err != nil {
				return errors.New("render upload interrupted")
			}
			if err := uploads[index].Close(); err != nil {
				return errors.New("render upload could not finish")
			}
			return nil
		})
	}
	group.Go(func() error {
		if err := command.Wait(); err != nil {
			return fmt.Errorf("render encoder stopped: %w", err)
		}
		return nil
	})
	if len(sources) > 0 {
		group.Go(func() error {
			defer func() {
				for _, writer := range rawWriters {
					_ = writer.Close()
				}
			}()
			for index, source := range sources {
				clip := exec.CommandContext(ctx, envOr("FFMPEG_PATH", "ffmpeg"), buildFFmpegClipArgs(source)...)
				clip.Stdout = rawWriters[index]
				var stderr cappedBuffer
				clip.Stderr = &stderr
				if err := clip.Run(); err != nil {
					return fmt.Errorf("render source stopped: %w", err)
				}
				_ = rawWriters[index].Close() // EOF advances concat to the next clip.
			}
			return nil
		})
	}
	err := group.Wait()
	if err != nil {
		for _, upload := range uploads {
			_ = upload.CloseWithError(err)
		}
	}
	return err
}

func (app *application) deleteRenderedObjects(names []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, name := range names {
		if err := app.storage.Bucket(app.cfg.Bucket).Object(name).Delete(ctx); err != nil {
			app.logger.Warn("temporary render cleanup pending", "error_code", "render_cleanup_pending")
		}
	}
}

func (app *application) streamRenderedMovies(ctx context.Context, names []string, id uuid.UUID, sources []renderSource) error {
	args := buildFFmpegRenderArgs(sources)
	var feeds []renderSource
	if len(names) == 2 {
		args = buildFFmpegPairedEncoderArgs()
		feeds = sources
	}
	err := runRenderPipeline(ctx, args, len(names), func(uploadCtx context.Context, index int) renderUpload {
		writer := app.storage.Bucket(app.cfg.Bucket).Object(names[index]).NewWriter(uploadCtx)
		writer.ChunkSize = 8 << 20
		writer.ContentType = "video/mp4"
		writer.CacheControl = "private, no-store"
		writer.CustomTime = time.Now().UTC()
		writer.Metadata = map[string]string{"renderId": id.String(), "temporary": "true", "deleteAfter": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
			"audioQuality": "aac-320k-default+lossless-alac-48khz", "videoQuality": "h264-1080p-crf18"}
		return writer
	}, feeds)
	if err != nil {
		app.deleteRenderedObjects(names)
	}
	return err
}
