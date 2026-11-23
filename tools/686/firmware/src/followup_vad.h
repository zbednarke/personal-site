#pragma once

#include <algorithm>
#include <cstdint>
#include <limits>

namespace ember {

// Energy measured in 20 ms blocks. Estimate the floor from the quietest
// block in each group so a short syllable does not raise it.
class FollowupVad {
 public:
  explicit FollowupVad(unsigned initialFloor = 4) : floor(initialFloor) {}

  void observeIdle(unsigned level) {
    groupMinimum = std::min(groupMinimum, level);
    if (++groupBlocks == 8) {
      const unsigned candidate = std::max(2u, groupMinimum);
      if (candidate < floor) {
        floor = (floor + candidate) / 2;
      } else {
        floor = (2 * floor + candidate + 2) / 3;
      }
      groupBlocks = 0;
      groupMinimum = std::numeric_limits<unsigned>::max();
    }
  }

  void beginWindow() {
    speechHistory = 0;
    highHistory = 0;
    groupBlocks = 0;
    groupMinimum = std::numeric_limits<unsigned>::max();
  }

  bool heardSpeech(unsigned level) {
    const unsigned threshold = onsetLevel();
    speechHistory = ((speechHistory << 1) | unsigned(level >= threshold)) & 0xff;
    highHistory = ((highHistory << 1) | unsigned(level >= threshold + std::max(2u, floor / 5))) & 0xff;
    if (__builtin_popcount(speechHistory) >= 4 && highHistory) return true;
    observeIdle(level);
    return false;
  }

  unsigned onsetLevel() const {
    return floor + std::max(5u, floor / 3);
  }

  unsigned keepListeningLevel() const {
    return floor + std::max(3u, floor / 6);
  }

 private:
  unsigned floor;
  unsigned groupMinimum = std::numeric_limits<unsigned>::max();
  unsigned groupBlocks = 0;
  unsigned speechHistory = 0;
  unsigned highHistory = 0;
};

}
