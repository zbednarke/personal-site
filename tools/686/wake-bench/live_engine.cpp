#include "wake_candidate.h"
#include "wake_word.h"

#include <algorithm>
#include <memory>

namespace {
struct LiveWake {
  ember::WakeCandidate gate;
  wake::Model model{};
  wake::Detector detector;
  unsigned threshold = 0;
  unsigned floor = 4 * 256;
};
}

extern "C" {
void *ember_live_create(const uint8_t *serialized, size_t length, unsigned threshold) {
  auto state = std::make_unique<LiveWake>();
  state->threshold = threshold;
  for (auto &example : state->model.examples) {
    if (!length) return nullptr;
    example.length = *serialized++;
    --length;
    const size_t size = example.length * sizeof(wake::Frame);
    if (example.length > wake::maxFrames || length < size) return nullptr;
    std::copy_n(serialized, size, reinterpret_cast<uint8_t *>(example.frames));
    serialized += size;
    length -= size;
  }
  if (length) return nullptr;
  state->detector.reset();
  return state.release();
}

void ember_live_destroy(void *handle) {
  delete static_cast<LiveWake *>(handle);
}

unsigned ember_live_gate(void *handle, const int16_t *pcm) {
  return static_cast<unsigned>(static_cast<LiveWake *>(handle)->gate.feed(wake::level(pcm)));
}

void ember_live_reset(void *handle) {
  auto &s = *static_cast<LiveWake *>(handle);
  s.detector.reset();
  s.floor = 4 * 256;
}

bool ember_live_match(void *handle, const int16_t *pcm) {
  auto &s = *static_cast<LiveWake *>(handle);
  const unsigned level = wake::level(pcm);
  const bool speech = level >= std::max(8u, 3 * s.floor / 256);
  if (!speech) s.floor = std::max(2u * 256, (63 * s.floor + 256 * level) / 64);
  return s.detector.feed(s.model, wake::features(pcm), speech, s.threshold);
}
}
