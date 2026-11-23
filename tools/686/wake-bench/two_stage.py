"""Run local speech segmentation, then compare phrase-detector voting rules."""

import argparse
import json
from pathlib import Path
import subprocess
import tempfile

from bench import ROOT, make_spotters, sherpa_result

BLOCK_BYTES = 640
ENGINES = ("ember", "sherpa", "sherpa_phones")


def replay(directory, threshold, labels):
    original = json.loads((directory / "results.json").read_text())
    models = make_spotters()
    rows = []
    with tempfile.TemporaryDirectory() as scratch:
        segment_file = Path(scratch) / "segment.pcm"
        for result, label in zip(original, labels, strict=True):
            path = directory / f"{result['index'] + 1:02d}.pcm"
            pcm = path.read_bytes()
            native = str(ROOT / "compare")
            segments = subprocess.check_output([native, "--segments", str(path)], text=True)
            votes = set()
            windows = []
            for record in segments.splitlines():
                first, last, truncated = map(int, record.split())
                samples = pcm[first * BLOCK_BYTES:last * BLOCK_BYTES]
                if not samples:
                    continue
                segment_file.write_bytes(samples)
                score = subprocess.check_output(
                    [native, str(directory / "enrollment.bin"), str(segment_file), str(threshold)], text=True
                )
                decisions = (
                    int(score.split()[0]) != 0,
                    "HEY_343" in sherpa_result(samples, models[0]),
                    "HEY_343" in sherpa_result(samples, models[1]),
                )
                detected = [engine for engine, match in zip(ENGINES, decisions) if match]
                votes.update(detected)
                windows.append({"start_ms": first * 20, "end_ms": last * 20,
                                "truncated": bool(truncated), "votes": detected})
            rows.append({
                "clip": result["index"] + 1, "truth": label,
                "full_clip_votes": [key for key in ENGINES if result[key]],
                "candidates": windows,
            })
    return rows


def tally(rows, field, minimum, truth):
    chosen = [row for row in rows if row["truth"] == truth]
    if field == "full_clip_votes":
        matches = sum(len(row[field]) >= minimum for row in chosen)
    else:
        matches = sum(any(len(window["votes"]) >= minimum for window in row[field]) for row in chosen)
    return f"{matches}/{len(chosen)}"


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--threshold", type=int, required=True)
    args = parser.parse_args()
    labels = json.loads((args.directory / "labels.json").read_text())["labels"]
    rows = replay(args.directory, args.threshold, labels)
    (args.directory / "two-stage.json").write_text(json.dumps(rows, indent=2) + "\n")
    for threshold in (1, 2, 3):
        for field in ("full_clip_votes", "candidates"):
            print(f"{threshold}/3 {field}: {tally(rows, field, threshold, 'wake')} wakes, "
                  f"{tally(rows, field, threshold, 'negative')} false wakes")
    print("Stage-one coverage:", sum(bool(row["candidates"]) for row in rows), "of", len(rows), "clips")
