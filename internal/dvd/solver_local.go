package dvd

import (
	"math"
	"time"
)

// localSearchSolver ports LocalSearchSolver.
type localSearchSolver struct {
	encoder *dvdEccBlockEncoder
}

func newLocalSearchSolver() *localSearchSolver {
	return &localSearchSolver{encoder: newDvdEccBlockEncoder()}
}

func (s *localSearchSolver) solve(constraint *imageConstraint, initialPayloads []byte, initialLba, psnOffset uint32, initialBoundary modulationBoundary, options solverOptions, progress func(iteration, current, best int)) (solverResult, error) {
	if len(initialPayloads) != dvdPayloadBytesPerBlock {
		return solverResult{}, newArgumentErrorParam("Initial payload must contain one complete 16-sector ECC Block.", "initialPayloads")
	}
	if options.Iterations < 0 ||
		options.MutationBytes < 1 ||
		math.IsNaN(options.InitialTemperature) || math.IsInf(options.InitialTemperature, 0) ||
		options.InitialTemperature <= 0 ||
		math.IsNaN(options.FinalTemperature) || math.IsInf(options.FinalTemperature, 0) ||
		options.FinalTemperature <= 0 {
		return solverResult{}, &argumentOutOfRangeError{param: "options", msg: "Iterations must be non-negative; mutation bytes and temperatures must be positive."}
	}

	random := newDotnetRandom(int32(options.RandomSeed))
	currentPayload := append([]byte(nil), initialPayloads...)
	currentEncoded, err := s.encoder.encode(currentPayload, initialLba, psnOffset, initialBoundary)
	if err != nil {
		return solverResult{}, err
	}
	currentScore, err := constraint.score(currentEncoded.ChannelLevels)
	if err != nil {
		return solverResult{}, err
	}
	bestPayload := append([]byte(nil), currentPayload...)
	bestEncoded := currentEncoded
	bestScore := currentScore
	stopwatchStart := time.Now()
	completed := 0

	mutationCount := minInt(options.MutationBytes, len(currentPayload))
	indexes := make([]int, mutationCount)
	previous := make([]byte, mutationCount)
	usedIndexes := map[int]struct{}{}
	for iteration := 0; iteration < options.Iterations && bestScore != 0; iteration++ {
		for k := range usedIndexes {
			delete(usedIndexes, k)
		}
		for mutation := 0; mutation < mutationCount; mutation++ {
			var index int
			for {
				index = constraint.samplePayloadIndex(random)
				if _, used := usedIndexes[index]; used {
					continue
				}
				usedIndexes[index] = struct{}{}
				break
			}
			indexes[mutation] = index
			previous[mutation] = currentPayload[index]
			var replacement byte
			for {
				replacement = byte(random.nextMax(256))
				if replacement != previous[mutation] {
					break
				}
			}
			currentPayload[index] = replacement
		}

		candidateEncoded, err := s.encoder.encode(currentPayload, initialLba, psnOffset, initialBoundary)
		if err != nil {
			return solverResult{}, err
		}
		candidateScore, err := constraint.score(candidateEncoded.ChannelLevels)
		if err != nil {
			return solverResult{}, err
		}
		fraction := 1.0
		if options.Iterations > 1 {
			fraction = float64(iteration) / float64(options.Iterations-1)
		}
		temperature := options.InitialTemperature *
			math.Pow(options.FinalTemperature/options.InitialTemperature, fraction)
		accept := candidateScore <= currentScore ||
			random.nextDouble() < math.Exp(float64(currentScore-candidateScore)/math.Max(temperature, 1e-9))
		if accept {
			currentScore = candidateScore
			currentEncoded = candidateEncoded
			if candidateScore < bestScore {
				bestScore = candidateScore
				bestPayload = append([]byte(nil), currentPayload...)
				bestEncoded = candidateEncoded
			}
		} else {
			for mutation := 0; mutation < mutationCount; mutation++ {
				currentPayload[indexes[mutation]] = previous[mutation]
			}
		}
		completed = iteration + 1
		if progress != nil {
			progress(completed, currentScore, bestScore)
		}
	}

	return solverResult{
		Payloads:            bestPayload,
		Encoded:             bestEncoded,
		Score:               bestScore,
		ScoreMaximum:        constraint.constrainedBits,
		ScoreMetric:         "bit-mismatch",
		CompletedIterations: completed,
		Elapsed:             time.Since(stopwatchStart),
	}, nil
}
