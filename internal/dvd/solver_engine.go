package dvd

// solverEngineSolve ports SolverEngine.Solve.
func solverEngineSolve(constraint *imageConstraint, initialPayloads []byte, initialLba, psnOffset uint32, initialBoundary modulationBoundary, options solverOptions, progress func(iteration, current, best int)) (solverResult, error) {
	switch options.Algorithm {
	case solverAlgorithmFull:
		return newLocalSearchSolver().solve(constraint, initialPayloads, initialLba, psnOffset, initialBoundary, options, progress)
	case solverAlgorithmStateControl:
		return (&stateControlSolver{}).solve(constraint, initialPayloads, initialLba, psnOffset, initialBoundary, options, progress)
	case solverAlgorithmDispersion:
		return (&dispersionPoolSolver{}).solve(constraint, initialPayloads, initialLba, psnOffset, initialBoundary, options, progress)
	default:
		return solverResult{}, &argumentOutOfRangeError{param: "options", msg: "Unknown solver algorithm."}
	}
}
