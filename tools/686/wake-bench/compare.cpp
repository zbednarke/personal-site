// Replay PCM through the same acoustic features, speech gate and matcher as Ember.
#include "wake_word.h"
#include "wake_candidate.h"

#include <algorithm>
#include <array>
#include <fstream>
#include <iostream>

int segments(const char *file) {
  using Gate = ember::WakeCandidate;
  std::ifstream audio(file, std::ios::binary);
  if (!audio) return 2;
  Gate candidate;
  std::array<int16_t, 320> samples{};
  unsigned block = 0;
  unsigned start = 0;
  bool pending = false;
  while (audio.read(reinterpret_cast<char *>(samples.data()), sizeof(samples))) {
    ++block;
    const auto event = candidate.feed(wake::level(samples.data()));
    if (event == Gate::Event::Started) {
      start = block > Gate::preRollBlocks ? block - Gate::preRollBlocks : 0;
      pending = true;
    }
    if (pending && (event == Gate::Event::Ended || event == Gate::Event::Truncated)) {
      std::cout << start << ' ' << block << ' ' << (event == Gate::Event::Truncated) << '\n';
      pending = false;
    }
  }
  if (pending) std::cout << start << ' ' << block << " 0\n";
  return 0;
}

int main(int argc, char **argv) {
  if (argc == 3 && std::string(argv[1]) == "--segments") return segments(argv[2]);
  if (argc != 4) return 2;
  std::ifstream enrolled(argv[1], std::ios::binary);
  std::ifstream audio(argv[2], std::ios::binary);
  if (!enrolled || !audio) return 2;
  const unsigned threshold = std::stoul(argv[3]);
  wake::Model model{};
  for (auto &example : model.examples) {
    const int length = enrolled.get();
    if (length < 0 || unsigned(length) > wake::maxFrames) return 2;
    example.length = length;
    if (!enrolled.read(reinterpret_cast<char *>(example.frames), length * sizeof(wake::Frame))) return 2;
  }
  if (enrolled.get() != EOF) return 2;
  wake::Detector detector;
  detector.reset();
  unsigned floor = 4 * 256;
  unsigned elapsed = 0;
  unsigned firstMatch = 0;
  wake::Match best;
  std::array<int16_t, 320> samples{};
  while (audio.read(reinterpret_cast<char *>(samples.data()), sizeof(samples))) {
    elapsed += 20;
    const unsigned level = wake::level(samples.data());
    const bool speech = level >= std::max(8u, 3 * floor / 256);
    if (!speech) floor = std::max(2u * 256, (63 * floor + 256 * level) / 64);
    wake::Match match;
    const bool found = detector.feed(model, wake::features(samples.data()), speech, threshold, &match);
    if (match.mean + match.worstSection < best.mean + best.worstSection) best = match;
    if (found && !firstMatch) firstMatch = elapsed;
  }
  std::cout << firstMatch << ' ' << best.mean << ' ' << best.worstSection << '\n';
}
