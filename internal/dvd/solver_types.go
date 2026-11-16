package dvd

import "time"

// SolverAlgorithm port.
type solverAlgorithm int

const (
	solverAlgorithmFull solverAlgorithm = iota
	solverAlgorithmStateControl
	solverAlgorithmDispersion
)

// solverOptions ports SolverOptions.
type solverOptions struct {
	Iterations         int
	MutationBytes      int
	RandomSeed         int32
	InitialTemperature float64
	FinalTemperature   float64
	Algorithm          solverAlgorithm
	BridgeBytes        int
	BeamWidth          int
	RetryBeamWidth     int
	RetryFrameFraction float64
	BatchFrames        int
	BatchCandidates    int
	SearchLanes        int
}

func (o solverOptions) adaptiveRetryEnabled() bool {
	return o.RetryBeamWidth > o.BeamWidth
}

// solverResult ports SolverResult.
type solverResult struct {
	Payloads                   []byte
	Encoded                    encodedEccBlock
	Score                      int
	ScoreMaximum               int
	ScoreMetric                string
	CompletedIterations        int
	Elapsed                    time.Duration
	GrayScore                  *int
	GrayScoreMaximum           *int
	GrayScoreMetric            *string
	ControlledScore            *int
	ControlledScoreMaximum     *int
	ControlledScoreMetric      *string
	ControlledGrayScore        *int
	ControlledGrayScoreMaximum *int
	ControlledGrayScoreMetric  *string
}

// multiBlockSolveOptions ports MultiBlockSolveOptions.
type multiBlockSolveOptions struct {
	StartLba              uint32
	FillSectors           uint32
	PsnOffset             uint32
	IterationsPerBlock    int
	MutationBytes         int
	RandomSeed            int32
	InitialTemperature    float64
	FinalTemperature      float64
	ZeroInitialPayload    bool
	ImageMapping          imageMappingOptions
	Algorithm             solverAlgorithm
	BridgeBytes           int
	BeamWidth             int
	RetryBeamWidth        int
	RetryFrameFraction    float64
	BatchFrames           int
	BatchCandidates       int
	SearchLanes           int
	FastParallelism       int
	PreserveExistingIso   bool
	UseSampleReuse        bool
	ReadCompatibility     bool
	ReadCompatibilityMode string
}

// multiBlockProgress ports MultiBlockProgress.
type multiBlockProgress struct {
	CompletedBlocks            int
	TotalBlocks                int
	BlockLba                   uint32
	Score                      int
	ScoreMaximum               int
	ScoreMetric                string
	FinalBoundary              modulationBoundary
	Elapsed                    time.Duration
	GrayScore                  *int
	GrayScoreMaximum           *int
	GrayScoreMetric            *string
	ControlledScore            *int
	ControlledScoreMaximum     *int
	ControlledGrayScore        *int
	ControlledGrayScoreMaximum *int
}

// multiBlockSolveResult ports MultiBlockSolveResult.
type multiBlockSolveResult struct {
	CompletedBlocks                 int
	FilledSectors                   uint32
	TotalScore                      int64
	TotalScoreMaximum               int64
	ScoreMetric                     string
	FinalBoundary                   modulationBoundary
	Elapsed                         time.Duration
	SparseIso                       bool
	IsoSectors                      uint32
	TotalGrayScore                  *int64
	TotalGrayScoreMaximum           *int64
	GrayScoreMetric                 *string
	TotalControlledScore            *int64
	TotalControlledScoreMaximum     *int64
	TotalControlledGrayScore        *int64
	TotalControlledGrayScoreMaximum *int64
}

// DvdReadCompatibilityModes constants (ported locally per the porting notes).
const (
	dvdReadCompatibilityOff          = "off"
	dvdReadCompatibilityLegacyBridge = "bridge-9b-v1"
	dvdReadCompatibilityPeriodic9B16 = "periodic-9b-16-v1"
	dvdReadCompatibilityCurrent      = dvdReadCompatibilityPeriodic9B16
)

func dvdReadCompatibilityModeIsSupported(mode string) bool {
	return mode == dvdReadCompatibilityLegacyBridge || mode == dvdReadCompatibilityPeriodic9B16
}

func intPtr(v int) *int            { return &v }
func longPtr(v int64) *int64       { return &v }
func doublePtr(v float64) *float64 { return &v }
func stringPtr(v string) *string   { return &v }
