#pragma once

#include "followup_vad.h"

namespace ember {

// A candidate marks a speech burst, independent of the keyword detector.
class WakeCandidate {
 public:
  enum class Event { None, Started, Ended, Truncated };
  static constexpr unsigned blockMs = 20;
  static constexpr unsigned preRollBlocks = 20;
  static constexpr unsigned maxBlocks = 250;

  Event feed(unsigned level) {
    if (warmup < preRollBlocks) {
      ++warmup;
      vad.observeIdle(level);
      return Event::None;
    }
    if (!active) {
      if (!vad.heardSpeech(level)) return Event::None;
      active = true;
      blocks = quiet = 0;
      return Event::Started;
    }

    ++blocks;
    quiet = level >= vad.keepListeningLevel() ? 0 : quiet + 1;
    if (quiet >= 45) {
      reset();
      return Event::Ended;
    }
    if (blocks == maxBlocks - preRollBlocks) return Event::Truncated;
    return Event::None;
  }

  void reset() {
    active = false;
    vad.beginWindow();
  }

  bool inSpeech() const { return active; }

 private:
  FollowupVad vad;
  unsigned warmup = 0;
  unsigned blocks = 0;
  unsigned quiet = 0;
  bool active = false;
};

}
