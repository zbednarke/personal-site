#pragma once

#include "followup_vad.h"

#include <array>

namespace ember {

// Keep several seconds of short-window minima so the wake phrase itself
// cannot immediately raise the threshold for the much quieter question.
class WakeQuestionAmbient {
 public:
  void observe(unsigned level) {
    groupMinimum = std::min(groupMinimum, level);
    if (++blocks == 10) {
      minima[next] = std::max(2u, groupMinimum);
      next = (next + 1) % minima.size();
      count = std::min(count + 1, minima.size());
      blocks = 0;
      groupMinimum = std::numeric_limits<unsigned>::max();
    }
  }

  FollowupVad snapshot() const {
    if (!count) return FollowupVad();
    return FollowupVad(*std::min_element(minima.begin(), minima.begin() + count));
  }

 private:
  std::array<unsigned, 20> minima{};
  size_t next = 0, count = 0;
  unsigned blocks = 0;
  unsigned groupMinimum = std::numeric_limits<unsigned>::max();
};

class WakeQuestion {
 public:
  static constexpr unsigned waitBlocks = 300; // 6 seconds to begin speaking.
  static constexpr unsigned leadBlocks = 16; // Preserve 320 ms before onset.
  static constexpr unsigned voicedRequired = 6;
  enum class Event { Continue, Started, Finished, NoSpeech };

  explicit WakeQuestion(FollowupVad idle) : vad(idle) {
    vad.beginWindow();
  }

  Event feed(unsigned level, unsigned block) {
    if (!started) {
      if (vad.heardSpeech(level)) {
        started = true;
        voiced = 4; // The onset gate required four voiced blocks.
        threshold = vad.keepListeningLevel();
        startBlock = block;
      }
    } else if (level >= threshold) {
      quiet = 0;
      if (++voiced == voicedRequired) return Event::Started;
    } else if (++quiet >= 60 && hasSpeech()) {
      return Event::Finished; // Allow a 1.2-second pause within a question.
    }

    // A brief sound must not consume the user's chance to ask a question.
    if (started && !hasSpeech() && block - startBlock >= 20) {
      started = false;
      voiced = quiet = 0;
      vad.beginWindow();
    }
    if (!started && block + 1 >= waitBlocks) return Event::NoSpeech;
    return Event::Continue;
  }

  bool hasSpeech() const { return voiced >= voicedRequired; }
  unsigned onsetBlock() const { return startBlock; }
  unsigned voicedBlocks() const { return voiced; }
  unsigned firstAudioBlock() const { return startBlock > leadBlocks ? startBlock - leadBlocks : 0; }

 private:
  FollowupVad vad;
  unsigned voiced = 0;
  unsigned quiet = 0;
  unsigned threshold = 0;
  unsigned startBlock = 0;
  bool started = false;
};

}
