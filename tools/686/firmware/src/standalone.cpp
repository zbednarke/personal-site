#include "standalone.h"
#include "wake_word.h"
#include "followup_vad.h"
#include "wake_candidate.h"
#include "wake_question.h"
#include "voice_adpcm.h"

#include <Preferences.h>
#include <WiFi.h>
#include <atomic>
#include <ctime>
#include <esp_crt_bundle.h>
#include <esp_http_client.h>
#include <esp_random.h>
#include <freertos/stream_buffer.h>
#include <memory>
#include <new>

namespace {
constexpr uint8_t buttonPin = 23;
constexpr uint8_t eyePin = 18;
constexpr size_t micFrames = 320;
constexpr size_t speakerFrames = 240;
constexpr size_t captureLimit = 600; // 12 seconds at 20 ms per block.
constexpr char responseType[] = "application/vnd.ember.turn.v1";
constexpr char devicePath[] = "/686/device/v1/turn";
struct DeviceConfig {
  String ssid, password, endpoint, token, ca;
};
DeviceConfig config;
bool configured = false;
bool armedMic = false;
wake::Model wakeModel = {};
wake::Detector detector;
constexpr uint32_t wakeModelVersion = 1;
unsigned quietLevel = 4 * 256;
constexpr unsigned defaultWakeSensitivity = 57;
unsigned wakeSensitivity = defaultWakeSensitivity;
uint32_t traceUntil = 0;
uint32_t traceNext = 0;
unsigned tracePeak = 0;
unsigned traceSpeech = 0;
wake::Match traceBest;

// Optional standalone shadow test. Persist only aggregate counts, never audio.
struct WakeFieldStats {
  uint32_t version = 1;
  uint32_t enabled = 0;
  uint32_t idleBlocks = 0;
  uint32_t speechBlocks = 0;
  uint32_t candidates = 0;
  uint32_t truncated = 0;
};

WakeFieldStats fieldStats;
ember::WakeCandidate fieldCandidate;
uint32_t checkpointBlocks = 0;
constexpr uint32_t fieldCheckpointInterval = 15 * 60 * 1000 / ember::WakeCandidate::blockMs;

bool saveFieldStats(const WakeFieldStats &value) {
  Preferences preferences;
  return preferences.begin("ember-field", false)
      && preferences.putBytes("stats", &value, sizeof(value)) == sizeof(value);
}

void loadFieldStats() {
  Preferences preferences;
  WakeFieldStats saved;
  if (preferences.begin("ember-field", true) && preferences.getBytesLength("stats") == sizeof(saved)
      && preferences.getBytes("stats", &saved, sizeof(saved)) == sizeof(saved) && saved.version == 1) {
    fieldStats = saved;
    checkpointBlocks = saved.idleBlocks;
  }
}

void countFieldCandidate(unsigned level) {
  ++fieldStats.idleBlocks;
  const auto event = fieldCandidate.feed(level);
  if (fieldCandidate.inSpeech()) ++fieldStats.speechBlocks;
  if (event == ember::WakeCandidate::Event::Started) ++fieldStats.candidates;
  if (event == ember::WakeCandidate::Event::Truncated) ++fieldStats.truncated;
  if (!fieldCandidate.inSpeech()
      && fieldStats.idleBlocks - checkpointBlocks >= fieldCheckpointInterval) {
    if (!saveFieldStats(fieldStats)) Serial.println("WAKE FIELD SAVE ERROR");
    checkpointBlocks = fieldStats.idleBlocks;
  }
}

void traceWake(const int16_t *pcm, bool speech, const wake::Match &match) {
  tracePeak = max(tracePeak, wake::level(pcm));
  traceSpeech += speech;
  if (match.mean + match.worstSection < traceBest.mean + traceBest.worstSection) traceBest = match;
  if (int32_t(millis() - traceNext) < 0) return;
  if (traceSpeech >= 3 || traceBest.mean != 999) {
    Serial.printf("WAKE SCORE mean=%u segment=%u peak=%u voiced=%u\n",
                  traceBest.mean, traceBest.worstSection, tracePeak, traceSpeech);
  }
  tracePeak = traceSpeech = 0;
  traceBest = {};
  traceNext = millis() + 850;
}

enum class Eye { Boot, Joining, Clock, Unenrolled, Ready, Listening, Thinking, Speaking, Followup, Done, Error, TrainWait, Usb };
std::atomic<Eye> eye{Eye::Usb};
uint32_t feedbackUntil = 0;
uint32_t retryMicAt = 0;

uint8_t breathing(uint32_t elapsed, uint32_t period, unsigned lo, unsigned hi) {
  const uint32_t phase = elapsed % period;
  const uint32_t ramp = phase < period / 2 ? phase : period - phase;
  return lo + (hi - lo) * ramp / (period / 2);
}

void eyeTask(void *) {
  Eye previous = Eye::Usb;
  uint32_t changedAt = millis();
  for (;;) {
    const Eye state = eye.load();
    if (state != previous) {
      previous = state;
      changedAt = millis();
    }
    const uint32_t elapsed = millis() - changedAt;
    unsigned brightness = 0;
    switch (state) {
      case Eye::Boot: brightness = 220; break;
      case Eye::Joining: brightness = elapsed % 1000 < 120 ? 170 : 5; break;
      case Eye::Clock: brightness = elapsed % 1000 < 120 || (elapsed % 1000 >= 240 && elapsed % 1000 < 360) ? 170 : 5; break;
      case Eye::Unenrolled: brightness = 24; break;
      case Eye::Ready: brightness = breathing(elapsed, 2800, 12, 65); break;
      case Eye::Listening: brightness = 255; break;
      case Eye::Thinking: brightness = breathing(elapsed, 820, 20, 145); break;
      case Eye::Followup: brightness = 115; break;
      case Eye::Done: brightness = elapsed < 140 ? 135 : 12; break;
      case Eye::Error: brightness = elapsed < 720 && elapsed % 240 < 100 ? 235 : 4; break;
      case Eye::TrainWait: brightness = elapsed % 260 < 100 ? 120 : 15; break;
      case Eye::Speaking:
      case Eye::Usb: break;
    }
    if (state != Eye::Usb && state != Eye::Speaking && eye.load() == state) ledcWrite(eyePin, brightness);
    vTaskDelay(pdMS_TO_TICKS(20));
  }
}

void showFeedback(Eye state) {
  eye.store(state);
  feedbackUntil = millis() + (state == Eye::Done ? 360 : 750);
}

bool isSpeech(const int16_t *pcm) {
  const unsigned level = wake::level(pcm);
  const bool active = level >= max(8u, 3 * quietLevel / 256);
  if (!active) quietLevel = max(2u * 256, (63 * quietLevel + 256 * level) / 64);
  return active;
}

// Preserve the original three-example NVS blob layout.
struct WakeBaseline {
  uint32_t version;
  wake::Example examples[wake::enrollmentCount];
};

struct Training {
  wake::Example examples[wake::enrollmentCount] = {};
  bool adding = false;
  unsigned example = 0;
  unsigned silence = 0;
  uint32_t startsAt = 0;
  uint32_t deadline = 0;
  bool active = false;
};
Training training;

void nextTrainingSample(uint32_t now) {
  training.examples[training.example].length = 0;
  training.silence = 0;
  training.startsAt = now + 1100;
  training.deadline = training.startsAt + 14000;
  eye.store(Eye::TrainWait);
  Serial.printf("WAKE SAMPLE %u OF %u: say Hey, three four three when the eye is solid blue\n",
                training.example + 1, unsigned(wake::enrollmentCount));
}

constexpr size_t maxExtraBytes = sizeof(training.examples);
constexpr char extraKey[] = "extra";

bool saveExtraSamples(Preferences &preferences) {
  // Store only voiced-length features. Reserving full-size slots in NVS
  // needlessly spends several kilobytes of the ESP32's default partition.
  std::unique_ptr<uint8_t[]> bytes(new (std::nothrow) uint8_t[maxExtraBytes]);
  if (!bytes) return false;
  size_t offset = 0;
  for (const auto &example : training.examples) {
    bytes[offset++] = example.length;
    const size_t frameBytes = size_t(example.length) * sizeof(wake::Frame);
    memcpy(bytes.get() + offset, example.frames, frameBytes);
    offset += frameBytes;
  }
  return preferences.putBytes(extraKey, bytes.get(), offset) == offset;
}

void loadExtraSamples(Preferences &preferences) {
  const size_t size = preferences.getBytesLength(extraKey);
  if (!size || size > maxExtraBytes) return;
  std::unique_ptr<uint8_t[]> bytes(new (std::nothrow) uint8_t[size]);
  if (!bytes || preferences.getBytes(extraKey, bytes.get(), size) != size) return;
  size_t offset = 0;
  for (auto &example : training.examples) {
    if (offset >= size) return;
    const size_t count = bytes[offset++];
    const size_t frameBytes = count * sizeof(wake::Frame);
    if (count < wake::minFrames || count > wake::maxFrames || frameBytes > size - offset) return;
    example.length = count;
    memcpy(example.frames, bytes.get() + offset, frameBytes);
    offset += frameBytes;
  }
  if (offset == size) memcpy(wakeModel.examples + wake::enrollmentCount, training.examples, sizeof(training.examples));
}

void updateTraining(const int16_t *pcm, bool speech) {
  const uint32_t now = millis();
  if (int32_t(now - training.startsAt) < 0) return;
  eye.store(Eye::Listening);
  auto &example = training.examples[training.example];
  if (!example.length && !speech) {
    if (int32_t(now - training.deadline) >= 0) {
      training.active = false;
      Serial.println("WAKE TRAIN ERROR: no speech heard");
      showFeedback(Eye::Error);
    }
    return;
  }
  if (example.length < wake::maxFrames) example.frames[example.length++] = wake::features(pcm);
  training.silence = speech ? 0 : training.silence + 1;
  if (training.silence < 23 && example.length < wake::maxFrames) return;
  if (training.silence < 23 || example.length - training.silence < wake::minFrames) {
    Serial.println("WAKE SAMPLE RETRY: say just the full phrase");
    nextTrainingSample(now);
    return;
  }
  example.length -= training.silence;
  if (++training.example < wake::enrollmentCount) {
    nextTrainingSample(now);
    return;
  }
  training.active = false;
  Preferences preferences;
  bool saved = preferences.begin("ember-wake", false);
  if (saved && training.adding) {
    saved = saveExtraSamples(preferences);
    if (saved) memcpy(wakeModel.examples + wake::enrollmentCount, training.examples, sizeof(training.examples));
  } else if (saved) {
    std::unique_ptr<WakeBaseline> baseline(new (std::nothrow) WakeBaseline{});
    saved = false;
    if (baseline) {
      baseline->version = wakeModelVersion;
      memcpy(baseline->examples, training.examples, sizeof(training.examples));
      saved = preferences.putBytes("model", baseline.get(), sizeof(WakeBaseline)) == sizeof(WakeBaseline);
    }
    if (saved) {
      wakeModel.version = wakeModelVersion;
      memcpy(wakeModel.examples, training.examples, sizeof(training.examples));
    }
  }
  if (!saved) {
    Serial.println("WAKE TRAIN ERROR: saving examples failed");
    showFeedback(Eye::Error);
    return;
  }
  detector.reset();
  Serial.println("WAKE TRAIN SAVED");
  showFeedback(Eye::Done);
}

void loadWakeModel() {
  Preferences preferences;
  std::unique_ptr<WakeBaseline> baseline(new (std::nothrow) WakeBaseline{});
  if (!baseline || !preferences.begin("ember-wake", true)
      || preferences.getBytesLength("model") != sizeof(WakeBaseline)
      || preferences.getBytes("model", baseline.get(), sizeof(WakeBaseline)) != sizeof(WakeBaseline)
      || baseline->version != wakeModelVersion) {
    wakeModel.version = 0;
    return;
  }
  wakeModel.version = baseline->version;
  memcpy(wakeModel.examples, baseline->examples, sizeof(baseline->examples));
  wakeSensitivity = constrain(preferences.getUChar("threshold", defaultWakeSensitivity), 42, 74);
  for (size_t i = 0; i < wake::enrollmentCount; ++i) {
    const size_t length = wakeModel.examples[i].length;
    if (length < wake::minFrames || length > wake::maxFrames) {
      wakeModel.version = 0;
      return;
    }
  }
  loadExtraSamples(preferences);
}

bool decodeConfig(const uint8_t *bytes, size_t length, DeviceConfig &target) {
  String *fields[] = { &target.ssid, &target.password, &target.endpoint, &target.token, &target.ca };
  size_t offset = 0;
  for (auto *field : fields) {
    if (length - offset < 2) return false;
    const size_t count = bytes[offset] | (size_t(bytes[offset + 1]) << 8);
    offset += 2;
    if (count > length - offset || (count && memchr(bytes + offset, 0, count))) return false;
    *field = String(reinterpret_cast<const char *>(bytes + offset), count);
    offset += count;
  }
  if (offset != length || !target.ssid.length() || target.ssid.length() > 32
      || target.password.length() > 63 || target.token.length() != 64
      || !target.endpoint.startsWith("https://") || !target.endpoint.endsWith(devicePath)
      || target.endpoint.length() > 220) return false;
  for (size_t i = 0; i < target.token.length(); ++i) {
    if (!isxdigit(static_cast<unsigned char>(target.token[i]))) return false;
  }
  return true;
}

void loadConfig() {
  Preferences preferences;
  if (!preferences.begin("ember-net", true)) return;
  const size_t length = preferences.getBytesLength("config");
  if (!length || length > 4096) return;
  std::unique_ptr<uint8_t[]> bytes(new (std::nothrow) uint8_t[length]);
  if (bytes && preferences.getBytes("config", bytes.get(), length) == length) {
    configured = decodeConfig(bytes.get(), length, config);
  }
}

bool networkReady() {
  return configured && WiFi.status() == WL_CONNECTED && time(nullptr) > 1700000000;
}

bool writeAll(esp_http_client_handle_t client, const void *data, size_t length) {
  const auto *bytes = static_cast<const char *>(data);
  while (length > 0) {
    const int written = esp_http_client_write(client, bytes, length);
    if (written <= 0 || size_t(written) > length) return false;
    bytes += written;
    length -= written;
  }
  return true;
}

bool readAll(esp_http_client_handle_t client, void *data, size_t length) {
  auto *bytes = static_cast<char *>(data);
  while (length > 0) {
    const int received = esp_http_client_read(client, bytes, length);
    if (received <= 0 || size_t(received) > length) return false;
    bytes += received;
    length -= received;
  }
  return true;
}

// Store the short follow-up lead-in in G.711-style companded PCM.
uint8_t encodeSample(int16_t sample) {
  unsigned magnitude = min(32635, abs(int(sample))) + 132;
  unsigned exponent = 0;
  for (unsigned bits = magnitude >> 8; bits > 1; bits >>= 1) ++exponent;
  return (sample < 0 ? 128 : 0) | (exponent << 4) | ((magnitude >> (exponent + 3)) & 15);
}

int16_t decodeSample(uint8_t sample) {
  const int magnitude = (((sample & 15) * 8 + 132) << ((sample >> 4) & 7)) - 132;
  return sample & 128 ? -magnitude : magnitude;
}

enum class Trigger { Button, Wake, Followup };

constexpr uint32_t followupWindowMs = 8000;
constexpr size_t preRollFrames = 12; // 240 ms at 20 ms per block.
ember::FollowupVad followupVad;
ember::WakeQuestionAmbient wakeQuestionAmbient;
struct Followup {
  uint32_t startedAt = 0;
  size_t next = 0;
  size_t count = 0;
  uint8_t audio[preRollFrames][micFrames];

  void reset() {
    startedAt = 0;
    next = count = 0;
  }

  bool feed(const int16_t *mono, unsigned level) {
    for (size_t i = 0; i < micFrames; ++i) audio[next][i] = encodeSample(mono[i]);
    next = (next + 1) % preRollFrames;
    count = min(count + 1, preRollFrames);
    return followupVad.heardSpeech(level);
  }
};
Followup followup;
bool openFollowup = false;
uint32_t liveLeaseUntil = 0;
bool liveWakePending = false;

bool liveWakeActive() {
  return liveLeaseUntil && int32_t(liveLeaseUntil - millis()) > 0;
}

constexpr size_t recordingBytesPerBlock = micFrames / 4;

struct Capture {
  uint8_t *audio;
  Trigger trigger;
  unsigned keepListeningLevel;
  std::atomic<size_t> blocks{0};
  std::atomic<size_t> firstAudioBlock{captureLimit};
  std::atomic<bool> done{false};
  const char *failure = nullptr; // Read only after done is published.
  ember::VoiceAdpcm encoder;
  int precedingSample = 0;

  void append(const int16_t *mono) {
    auto *next = audio + blocks.load() * recordingBytesPerBlock;
    // Low-pass before decimating to 8 kHz. Keep the preceding sample so the
    // filter is continuous across the start, pre-roll, and microphone blocks.
    for (size_t i = 0; i < micFrames; i += 4) {
      const int first = (precedingSample + 2 * int(mono[i]) + mono[i + 1]) / 4;
      const int second = (int(mono[i + 1]) + 2 * int(mono[i + 2]) + mono[i + 3]) / 4;
      precedingSample = mono[i + 3];
      const uint8_t low = encoder.encode(first);
      const uint8_t high = encoder.encode(second);
      next[i / 4] = low | (high << 4);
    }
    blocks.fetch_add(1);
  }
};

void captureTask(void *context) {
  auto &capture = *static_cast<Capture *>(context);
  uint32_t releasedAt = 0;
  unsigned quiet = 0;
  const bool following = capture.trigger == Trigger::Followup;
  const bool waking = capture.trigger == Trigger::Wake;
  ember::WakeQuestion question(wakeQuestionAmbient.snapshot());
  unsigned peakLevel = 0;
  int16_t mono[micFrames];
  for (size_t block = capture.blocks.load(); block < captureLimit; ++block) {
    if (capture.trigger == Trigger::Button) {
      if (digitalRead(buttonPin) == HIGH) {
        if (!releasedAt) releasedAt = millis();
        if (millis() - releasedAt >= 25) break;
      } else {
        releasedAt = 0;
      }
    }
    if (!readMicBlock(mono)) {
      capture.failure = "mic read";
      break;
    }
    if (waking) {
      const unsigned level = wake::level(mono);
      peakLevel = max(peakLevel, level);
      const auto event = question.feed(level, block);
      if (event == ember::WakeQuestion::Event::Started) {
        capture.firstAudioBlock.store(question.firstAudioBlock());
      } else if (event == ember::WakeQuestion::Event::Finished || event == ember::WakeQuestion::Event::NoSpeech) {
        break;
      }
    } else if (following) {
      quiet = wake::level(mono) >= capture.keepListeningLevel ? 0 : quiet + 1;
      if (quiet >= 60) break;
    }
    capture.append(mono);
  }
  if (waking) {
    if (!question.hasSpeech() && !capture.failure) capture.failure = "no speech within 6s";
    const auto first = capture.firstAudioBlock.load();
    Serial.printf("WIFI TURN WAKE CAPTURE recorded_ms=%u onset_ms=%d voiced_ms=%u peak_activity=%u trim_ms=%u\n",
                  unsigned(capture.blocks.load() * 20),
                  first == captureLimit ? -1 : int(question.onsetBlock() * 20),
                  question.voicedBlocks() * 20, peakLevel,
                  first == captureLimit ? 0u : unsigned(first * 20));
  }
  stopMic();
  eye.store(Eye::Thinking);
  capture.done.store(true);
  vTaskDelete(nullptr);
}

enum class Upload { Sent, Retryable, Rejected };

__attribute__((noinline)) Upload uploadMic(esp_http_client_handle_t client, Capture &capture) {
  // Decode in batches to retain the existing 16 kHz PCM transport.
  constexpr size_t batchBlocks = 8;
  constexpr size_t maxPayload = batchBlocks * micFrames * sizeof(int16_t);
  std::unique_ptr<uint8_t[]> wire(new (std::nothrow) uint8_t[12 + maxPayload + 2]);
  if (!wire) {
    Serial.println("WIFI TURN NO UPLOAD BUFFER");
    return Upload::Retryable;
  }
  while (capture.firstAudioBlock.load() == captureLimit && !capture.done.load()) delay(5);
  const size_t first = capture.firstAudioBlock.load();
  if (first == captureLimit) {
    Serial.printf("WIFI TURN CAPTURE FAILED: %s (%u ms)\n",
                  capture.failure ? capture.failure : "no speech", unsigned(capture.blocks.load() * 20));
    return Upload::Rejected;
  }
  size_t sent = first;
  int previousSample = 0;
  ember::VoiceAdpcm decoder;
  // Advance the decoder through the discarded ADPCM prefix so the first
  // uploaded sample still has the correct predictor state.
  for (size_t i = 0; i < first * recordingBytesPerBlock; ++i) {
    decoder.decode(capture.audio[i] & 15);
    previousSample = decoder.decode(capture.audio[i] >> 4);
  }
  for (;;) {
    const bool done = capture.done.load();
    const size_t remaining = capture.blocks.load() - sent;
    if (done && !remaining) break;
    if (!done && remaining < batchBlocks) {
      delay(5);
      continue;
    }
    const size_t count = min(remaining, batchBlocks);
    const auto *encoded = capture.audio + sent * recordingBytesPerBlock;
    size_t offset = 12;
    const auto writeSample = [&](uint16_t sample) {
      wire[offset++] = sample & 255;
      wire[offset++] = sample >> 8;
    };
    for (size_t i = 0; i < count * recordingBytesPerBlock; ++i) {
      for (uint8_t code : {uint8_t(encoded[i] & 15), uint8_t(encoded[i] >> 4)}) {
        const int sample = decoder.decode(code);
        writeSample((previousSample + sample) / 2);
        writeSample(sample);
        previousSample = sample;
      }
    }
    const size_t bytes = offset - 12;
    char prefix[12];
    const int header = snprintf(prefix, sizeof(prefix), "%x\r\n", static_cast<unsigned>(bytes));
    memcpy(wire.get() + 12 - header, prefix, header);
    memcpy(wire.get() + offset, "\r\n", 2);
    if (!writeAll(client, wire.get() + 12 - header, header + bytes + 2)) {
      Serial.println("WIFI TURN UPLOAD FAILED");
      return Upload::Retryable;
    }
    sent += count;
  }
  if (capture.failure || (sent - first) * micFrames * sizeof(int16_t) < 8000) {
    Serial.printf("WIFI TURN CAPTURE FAILED: %s (%u ms)\n",
                  capture.failure ? capture.failure : "too short", unsigned((sent - first) * 20));
    return Upload::Rejected;
  }
  Serial.printf("WIFI TURN CAPTURED %u ms\n", unsigned((sent - first) * 20));
  // Once the terminator is attempted, the backend may have committed the
  // turn even if this socket reports a failure. Never replay that attempt.
  return writeAll(client, "0\r\n\r\n", 5) ? Upload::Sent : Upload::Rejected;
}

constexpr size_t speakerBytesPerMs = 24 * sizeof(int16_t);
constexpr size_t startupPlaybackMs = 200;
constexpr size_t playbackCapacityMs = 480;
constexpr size_t speakerDmaMs = 100;

struct ReplyPlayback {
  StreamBufferHandle_t queue;
  std::atomic<bool> inputDone{false};
  std::atomic<bool> outputDone{false};
  bool failed = false; // Read after outputDone.
  uint32_t initialBufferMs = 0, refills = 0, maxWriteMs = 0, padSamples = 0;
};

void replyPlaybackTask(void *context) {
  auto &reply = *static_cast<ReplyPlayback *>(context);
  int16_t block[speakerFrames];
  size_t filled = 0;
  bool prebuffer = true;
  bool started = false;
  for (;;) {
    if (prebuffer) {
      while (!reply.inputDone.load()
             && xStreamBufferBytesAvailable(reply.queue) < startupPlaybackMs * speakerBytesPerMs) {
        delay(2);
      }
      if (!started) reply.initialBufferMs = xStreamBufferBytesAvailable(reply.queue) / speakerBytesPerMs;
      prebuffer = false;
    }
    const bool done = reply.inputDone.load();
    const size_t count = xStreamBufferReceive(
        reply.queue, reinterpret_cast<uint8_t *>(block) + filled,
        sizeof(block) - filled, done ? 0 : pdMS_TO_TICKS(speakerDmaMs));
    filled += count;
    if (!count && !done) {
      if (started) ++reply.refills;
      prebuffer = true;
      continue;
    }
    if (!count && !filled) break;
    if (filled != sizeof(block) && count) continue;

    eye.store(Eye::Speaking);
    const uint32_t writeAt = millis();
    if (!writeSpeakerBlock(block, filled / sizeof(block[0]))) {
      reply.failed = true;
      break;
    }
    reply.maxWriteMs = max(reply.maxWriteMs, millis() - writeAt);
    reply.padSamples += (sizeof(block) - filled) / sizeof(block[0]);
    started = true;
    filled = 0;
  }
  drainSpeaker();
  reply.outputDone.store(true);
  vTaskDelete(nullptr);
}

const char *replyHttpReason(esp_http_client_handle_t client) {
  // The hosted endpoint sends a small JSON error. Log known categories, never
  // arbitrary body data that may originate at the proxy or a provider.
  char body[256] = {};
  size_t size = 0;
  esp_http_client_set_timeout_ms(client, 1000);
  while (size < sizeof(body) - 1) {
    const int count = esp_http_client_read(client, body + size, sizeof(body) - size - 1);
    if (count <= 0) break;
    size += count;
  }
  struct Reason { const char *message; const char *code; };
  constexpr Reason reasons[] = {
    {"voice transcription returned no speech", "transcription_no_speech"},
    {"voice transcription provider is unavailable", "transcription_unavailable"},
    {"voice transcription failed", "transcription_failed"},
    {"conversation provider is unavailable", "conversation_unavailable"},
    {"conversation generation failed", "conversation_failed"},
    {"speech provider is unavailable", "synthesis_unavailable"},
    {"speech synthesis failed", "synthesis_failed"},
    {"invalid or incomplete microphone audio", "invalid_capture"},
    {"turn already active or used", "busy_or_duplicate"},
  };
  for (const auto &reason : reasons) {
    if (strstr(body, reason.message)) return reason.code;
  }
  return size ? "unclassified_response" : "empty_response";
}

bool playReply(esp_http_client_handle_t client, const bool &contentTypeOK) {
  const auto headers = esp_http_client_fetch_headers(client);
  const int status = esp_http_client_get_status_code(client);
  if (headers < 0 || status != 200) {
    Serial.printf("WIFI TURN HTTP %d reason=%s\n", status,
                  headers < 0 ? "response_headers_failed" : replyHttpReason(client));
    return false;
  }
  if (!contentTypeOK) {
    Serial.println("WIFI TURN PLAYBACK unexpected_content_type");
    return false;
  }

  ReplyPlayback reply{xStreamBufferCreate(playbackCapacityMs * speakerBytesPerMs, 1)};
  if (!reply.queue) {
    Serial.println("WIFI TURN PLAYBACK no_buffer");
    return false;
  }
  if (xTaskCreate(replyPlaybackTask, "ember-playback", 4096, &reply, 2, nullptr) != pdPASS) {
    vStreamBufferDelete(reply.queue);
    Serial.println("WIFI TURN PLAYBACK no_task");
    return false;
  }

  uint32_t frames = 0, audioBytes = 0, peak = 0, maxReadMs = 0, overBufferReads = 0;
  int16_t samples[1920];
  const char *result = "reply_read_failed";
  bool complete = false;
  uint8_t header[3];
  while (!reply.outputDone.load()) {
    const uint32_t readAt = millis();
    if (!readAll(client, header, sizeof(header))) break;
    const size_t length = (size_t(header[1]) << 8) | header[2];
    if (header[0] == 'D' && length == 0 && frames) {
      complete = true;
      result = "done";
      break;
    }
    if (header[0] == 'E') {
      result = "backend_stream_error";
      break;
    }
    if (header[0] != 'A' || !length || length > sizeof(samples) || length % 2) {
      result = "invalid_frame";
      break;
    }
    if (!readAll(client, samples, length)) break;
    if (frames) {
      const uint32_t elapsed = millis() - readAt;
      maxReadMs = max(maxReadMs, elapsed);
      if (elapsed > speakerDmaMs) ++overBufferReads;
    }
    for (size_t i = 0; i < length / sizeof(samples[0]); ++i) {
      peak = max(peak, uint32_t(abs(int(samples[i]))));
    }
    ++frames;
    audioBytes += length;
    const auto *bytes = reinterpret_cast<const uint8_t *>(samples);
    size_t sent = 0;
    while (sent < length && !reply.outputDone.load()) {
      sent += xStreamBufferSend(reply.queue, bytes + sent, length - sent, pdMS_TO_TICKS(25));
    }
  }
  reply.inputDone.store(true);
  while (!reply.outputDone.load()) delay(2);
  vStreamBufferDelete(reply.queue);
  if (reply.failed) result = "i2s_write_failed";
  Serial.printf("WIFI TURN PLAYBACK %s frames=%u audio_ms=%u peak_pcm=%u pad_samples=%u "
                "max_read_ms=%u reads_over_100ms=%u max_i2s_write_ms=%u prebuffer_ms=%u refills=%u\n",
                result, unsigned(frames), unsigned(audioBytes / speakerBytesPerMs), unsigned(peak),
                unsigned(reply.padSamples), unsigned(maxReadMs), unsigned(overBufferReads),
                unsigned(reply.maxWriteMs), unsigned(reply.initialBufferMs), unsigned(reply.refills));
  return complete && !reply.failed;
}

__attribute__((noinline)) bool runTurn(Trigger trigger) {
  constexpr size_t recordingBytes = captureLimit * recordingBytesPerBlock;
  std::unique_ptr<uint8_t[]> recording(new (std::nothrow) uint8_t[recordingBytes]);
  if (!recording) {
    Serial.printf("WIFI TURN NO RECORDING BUFFER (%u bytes)\n", unsigned(recordingBytes));
    return false;
  }
  Capture capture{recording.get(), trigger, followupVad.keepListeningLevel()};
  if (trigger != Trigger::Wake) capture.firstAudioBlock.store(0);
  if (trigger == Trigger::Followup) {
    int16_t mono[micFrames];
    for (size_t i = 0; i < followup.count; ++i) {
      const size_t slot = (followup.next + preRollFrames - followup.count + i) % preRollFrames;
      for (size_t j = 0; j < micFrames; ++j) mono[j] = decodeSample(followup.audio[slot][j]);
      capture.append(mono);
    }
  }
  followup.reset();
  eye.store(Eye::Listening);
  ledcWrite(eyePin, 255);
  if (xTaskCreate(captureTask, "ember-capture", 7168, &capture, 2, nullptr) != pdPASS) {
    Serial.println("WIFI TURN NO CAPTURE TASK");
    return false;
  }

  esp_http_client_config_t settings = {};
  settings.url = config.endpoint.c_str();
  settings.method = HTTP_METHOD_POST;
  settings.disable_auto_redirect = true;
  settings.timeout_ms = 3500;
  bool contentTypeOK = false;
  settings.user_data = &contentTypeOK;
  settings.event_handler = [](esp_http_client_event_t *event) -> esp_err_t {
    if (event->event_id == HTTP_EVENT_ON_HEADER && !strcasecmp(event->header_key, "Content-Type")) {
      *static_cast<bool *>(event->user_data) = strcmp(event->header_value, responseType) == 0;
    }
    return ESP_OK;
  };
  if (config.ca.length()) settings.cert_pem = config.ca.c_str();
  else settings.crt_bundle_attach = esp_crt_bundle_attach;
  bool success = false;
  for (unsigned attempt = 0; attempt < 2; ++attempt) {
    contentTypeOK = false;
    auto client = esp_http_client_init(&settings);
    Upload uploaded = Upload::Retryable;
    if (client) {
      uint8_t random[16];
      esp_fill_random(random, sizeof(random));
      char turnID[33] = {};
      for (size_t i = 0; i < sizeof(random); ++i) snprintf(turnID + i * 2, 3, "%02x", random[i]);
      const String bearer = "Bearer " + config.token;
      esp_http_client_set_header(client, "Authorization", bearer.c_str());
      esp_http_client_set_header(client, "Content-Type", "application/octet-stream");
      esp_http_client_set_header(client, "X-Ember-Turn-ID", turnID);
      const uint32_t connectStarted = millis();
      if (esp_http_client_open(client, -1) == ESP_OK) {
        Serial.printf("WIFI TURN HTTPS %u ms, recorded %u ms\n",
                      unsigned(millis() - connectStarted), unsigned(capture.blocks.load() * 20));
        uploaded = uploadMic(client, capture);
      } else {
        Serial.println("WIFI TURN CONNECT FAILED");
      }
      if (uploaded == Upload::Sent) {
        recording.reset();
        esp_http_client_set_timeout_ms(client, 22000);
        success = playReply(client, contentTypeOK);
      }
      stopSpeaker();
      esp_http_client_cleanup(client);
    }
    if (uploaded != Upload::Retryable || attempt != 0) break;
    // Keep the eye solid and let the recorder finish before retrying.
    while (!capture.done.load()) delay(5);
    if (capture.failure || capture.blocks.load() * micFrames * sizeof(int16_t) < 8000) break;
    Serial.println("WIFI TURN RETRYING SAVED RECORDING");
  }
  while (!capture.done.load()) delay(5);
  return success;
}

} // namespace

void setupStandalone() {
  pinMode(buttonPin, INPUT_PULLUP);
  loadConfig();
  if (!configured) return;
  loadWakeModel();
  loadFieldStats();
  detector.reset();
  eye.store(Eye::Boot);
  xTaskCreate(eyeTask, "ember-eye", 2048, nullptr, 1, nullptr);
  WiFi.persistent(false);
  WiFi.mode(WIFI_STA);
  WiFi.setAutoReconnect(true);
  WiFi.begin(config.ssid.c_str(), config.password.c_str());
  configTime(0, 0, "time.cloudflare.com", "pool.ntp.org");
}

bool standaloneOwnsEye() {
  return configured && eye.load() != Eye::Usb;
}

bool standaloneOwnsMic() {
  return armedMic;
}

void suspendStandaloneMic() {
  if (!armedMic) return;
  stopMic();
  armedMic = false;
  fieldCandidate.reset();
  detector.reset();
}

void updateStandalone(bool audioInUse) {
  if (!configured) return;
  static bool previous = false, stable = false;
  static uint32_t changedAt = 0;
  const bool pressed = digitalRead(buttonPin) == LOW;
  const uint32_t now = millis();
  if (pressed != previous) {
    changedAt = now;
    previous = pressed;
  }
  bool buttonTriggered = false;
  if (now - changedAt >= 25 && stable != pressed) {
    stable = pressed;
    buttonTriggered = pressed;
  }
  if (audioInUse) {
    openFollowup = false;
    followup.reset();
    suspendStandaloneMic();
    eye.store(Eye::Usb);
    return;
  }
  if (int32_t(now - feedbackUntil) < 0) return;
  if (now < 280) return;
  if (!networkReady() && !training.active) {
    openFollowup = false;
    followup.reset();
    suspendStandaloneMic();
    eye.store(WiFi.status() == WL_CONNECTED ? Eye::Clock : Eye::Joining);
    return;
  }
  if (!armedMic) {
    if (int32_t(now - retryMicAt) < 0) return;
    if (!startMic()) {
      retryMicAt = now + 1500;
      showFeedback(Eye::Error);
      return;
    }
    armedMic = true;
  }

  if (openFollowup && !followup.startedAt) {
    followup.startedAt = millis();
    followupVad.beginWindow();
    eye.store(Eye::Followup);
    Serial.println("FOLLOWUP READY");
  }
  if (openFollowup && millis() - followup.startedAt >= followupWindowMs) {
    openFollowup = false;
    followup.reset();
    Serial.println("FOLLOWUP EXPIRED");
    showFeedback(Eye::Done);
    return;
  }
  const bool hostOwnsWake = liveWakeActive() && !openFollowup && !training.active && !fieldStats.enabled;
  bool wakeTriggered = hostOwnsWake && liveWakePending;
  liveWakePending = false;
  if (hostOwnsWake) {
    eye.store(Eye::Ready);
    if (!buttonTriggered && !wakeTriggered) return;
  }
  bool followupTriggered = false;
  if (!hostOwnsWake && (!buttonTriggered || training.active)) {
    int16_t mono[micFrames];
    if (!readMicBlock(mono)) {
      suspendStandaloneMic();
      showFeedback(Eye::Error);
      return;
    }
    if (training.active) {
      updateTraining(mono, isSpeech(mono));
      return;
    }
    const unsigned level = wake::level(mono);
    if (openFollowup) {
      followupTriggered = followup.feed(mono, level);
    } else {
      followupVad.observeIdle(level);
      wakeQuestionAmbient.observe(level);
      eye.store(wakeModel.version ? Eye::Ready : Eye::Unenrolled);
      if (fieldStats.enabled) {
        countFieldCandidate(level);
      } else if (wakeModel.version) {
        const bool speech = isSpeech(mono);
        const bool tracing = int32_t(traceUntil - millis()) > 0;
        wake::Match match;
        wakeTriggered = detector.feed(wakeModel, wake::features(mono), speech, wakeSensitivity,
                                      tracing ? &match : nullptr);
        if (tracing) traceWake(mono, speech, match);
      }
    }
  }
  if (!buttonTriggered && !wakeTriggered && !followupTriggered) return;
  const Trigger trigger = buttonTriggered ? Trigger::Button : wakeTriggered ? Trigger::Wake : Trigger::Followup;
  if (trigger == Trigger::Followup) {
    Serial.printf("FOLLOWUP SPEECH onset=%u continue=%u\n", followupVad.onsetLevel(), followupVad.keepListeningLevel());
  } else {
    Serial.println(trigger == Trigger::Button ? "BUTTON PRESS" : "WAKE MATCH");
  }
  openFollowup = false;
  fieldCandidate.reset();
  const bool success = runTurn(trigger);
  stopMic();
  armedMic = false;
  detector.reset();
  Serial.println(success ? "WIFI TURN DONE" : "WIFI TURN FAILED");
  followup.reset();
  if (success) openFollowup = true;
  else showFeedback(Eye::Error);
}

bool handleStandaloneCommand(const String &command) {
  if (command == "WAKE LIVE END") {
    liveLeaseUntil = 0;
    liveWakePending = false;
    detector.reset();
    Serial.println("LIVE CLOSED");
    return true;
  }
  if (command == "WAKE LIVE BEGIN" || command == "WAKE LIVE READ" || command == "WAKE LIVE TRIGGER") {
    if (!configured || !wakeModel.version || fieldStats.enabled || training.active
        || (command != "WAKE LIVE BEGIN" && !liveWakeActive())) {
      Serial.println("LIVE UNAVAILABLE");
      return true;
    }
    liveLeaseUntil = millis() + 3500;
    if (!networkReady() || !armedMic || openFollowup || int32_t(millis() - feedbackUntil) < 0) {
      Serial.println("LIVE BUSY");
      return true;
    }
    if (command == "WAKE LIVE BEGIN") {
      detector.reset();
      Serial.println("LIVE READY");
    } else if (command == "WAKE LIVE TRIGGER") {
      liveWakePending = true;
      Serial.println("LIVE ACCEPTED");
    } else {
      int16_t pcm[2 * micFrames];
      if (!readMicBlock(pcm) || !readMicBlock(pcm + micFrames)) {
        suspendStandaloneMic();
        Serial.println("LIVE MIC ERROR");
      } else {
        wakeQuestionAmbient.observe(wake::level(pcm));
        wakeQuestionAmbient.observe(wake::level(pcm + micFrames));
        Serial.println("LIVE PCM 1280");
        Serial.write(reinterpret_cast<const uint8_t *>(pcm), sizeof(pcm));
      }
    }
    return true;
  }
  if (command == "WAKE FIELD STATUS") {
    Serial.printf("WAKE FIELD %s idle_ms=%llu speech_ms=%llu candidates=%u truncated=%u\n",
                  fieldStats.enabled ? "ON" : "OFF",
                  static_cast<unsigned long long>(fieldStats.idleBlocks) * ember::WakeCandidate::blockMs,
                  static_cast<unsigned long long>(fieldStats.speechBlocks) * ember::WakeCandidate::blockMs,
                  unsigned(fieldStats.candidates), unsigned(fieldStats.truncated));
    return true;
  }
  if (command == "WAKE FIELD START" || command == "WAKE FIELD STOP") {
    const bool start = command == "WAKE FIELD START";
    WakeFieldStats next = start ? WakeFieldStats{} : fieldStats;
    next.enabled = start;
    if (!configured || !saveFieldStats(next)) {
      Serial.println("WAKE FIELD SAVE ERROR");
      return true;
    }
    fieldStats = next;
    checkpointBlocks = fieldStats.idleBlocks;
    fieldCandidate = ember::WakeCandidate{};
    detector.reset();
    Serial.println(start ? "WAKE FIELD STARTED" : "WAKE FIELD STOPPED");
    return true;
  }
  if (command == "WAKE EXPORT") {
    if (!wakeModel.version) {
      Serial.println("WAKE EXPORT EMPTY");
      return true;
    }
    Serial.printf("WAKE MODEL %u %u\n", wakeSensitivity, unsigned(wake::exampleCount));
    for (const auto &example : wakeModel.examples) {
      Serial.write(example.length);
      Serial.write(reinterpret_cast<const uint8_t *>(example.frames), example.length * sizeof(wake::Frame));
    }
    return true;
  }
  if (command == "WAKE TRACE") {
    traceUntil = millis() + 55000;
    traceNext = millis() + 850;
    tracePeak = traceSpeech = 0;
    traceBest = {};
    Serial.printf("WAKE TRACE ON threshold=%u segment_limit=%u examples=", wakeSensitivity, wakeSensitivity + 17);
    for (const auto &example : wakeModel.examples) Serial.printf(" %u", example.length);
    Serial.println();
    return true;
  }
  if (command.startsWith("WAKE SENSITIVITY ")) {
    const char *value = command.c_str() + 17;
    char *end = nullptr;
    const long threshold = strtol(value, &end, 10);
    Preferences preferences;
    if (end == value || *end || threshold < 42 || threshold > 74
        || !preferences.begin("ember-wake", false) || preferences.putUChar("threshold", threshold) != 1) {
      Serial.println("WAKE SENSITIVITY ERROR");
    } else {
      wakeSensitivity = threshold;
      Serial.printf("WAKE SENSITIVITY %u\n", wakeSensitivity);
    }
    return true;
  }
  if (command == "WAKE STATUS") {
    Serial.println(training.active ? "WAKE TRAINING" : wakeModel.version ? "WAKE READY" : "WAKE NEEDS ENROLLMENT");
    return true;
  }
  if (command == "WAKE TRAIN" || command == "WAKE ADD") {
    const bool adding = command == "WAKE ADD";
    if (!configured || training.active || (adding && !wakeModel.version)) {
      Serial.println("WAKE TRAIN ERROR: setup unavailable");
      return true;
    }
    openFollowup = false;
    followup.reset();
    training.adding = adding;
    training.example = 0;
    training.active = true;
    nextTrainingSample(millis());
    return true;
  }
  if (command == "WAKE CANCEL") {
    training.active = false;
    detector.reset();
    Serial.println("WAKE TRAIN CANCELLED");
    return true;
  }
  if (command == "WIFI STATUS") {
    if (!configured) Serial.println("WIFI UNPROVISIONED");
    else Serial.println(networkReady() ? "WIFI READY" : "WIFI CONNECTING");
    return true;
  }
  if (!command.startsWith("WIFI SET ")) return false;
  const long length = command.substring(9).toInt();
  if (length < 10 || length > 4096) {
    Serial.println("WIFI CONFIG ERROR");
    return true;
  }
  std::unique_ptr<uint8_t[]> bytes(new (std::nothrow) uint8_t[length]);
  DeviceConfig next;
  Serial.setTimeout(2500);
  Preferences preferences;
  if (!bytes || Serial.readBytes(bytes.get(), length) != static_cast<size_t>(length)
      || !decodeConfig(bytes.get(), length, next)
      || !preferences.begin("ember-net", false)
      || preferences.putBytes("config", bytes.get(), length) != static_cast<size_t>(length)) {
    Serial.println("WIFI CONFIG ERROR");
    return true;
  }
  preferences.end();
  Serial.println("WIFI CONFIG SAVED");
  Serial.flush();
  delay(140);
  ESP.restart();
  return true;
}
