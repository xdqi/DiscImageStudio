// Package iso ports DiscImageStudio.Iso: the ISO9660/Joliet image writer and
// the UDF 2.01 image writer, plus the filesystem metadata budget constants.
//
// This file pins the package API consumed by the cd, dvd and bluray packages.
package iso

import (
	"time"
)

// LogicalSectorBytes is the 2048-byte sector size shared by both writers.
const LogicalSectorBytes = 2048

// MaximumJolietVolumeLabelCharacters is the UCS-2 volume label limit.
const MaximumJolietVolumeLabelCharacters = 16

// UdfMinimumVolumeSectors is the smallest drawing volume the UDF writer plans.
const UdfMinimumVolumeSectors uint32 = 640

// Filesystem metadata budget constants.
const (
	MaxExtentsPerFile        = 32
	WorstBytesPerExtraExtent = 320
	MaximumReserveSectors    = 4096
)

// MetadataReserveSectors ports FilesystemMetadataBudget.ReserveSectors.
func MetadataReserveSectors(fileCount int) int {
	if fileCount <= 0 {
		return 0
	}
	bytes := int64(fileCount) * (MaxExtentsPerFile - 1) * WorstBytesPerExtraExtent
	sectors := int((bytes + LogicalSectorBytes - 1) / LogicalSectorBytes)
	if sectors > MaximumReserveSectors {
		return MaximumReserveSectors
	}
	return sectors
}

// IsoLayoutPlan is a planned ISO9660/Joliet volume (Iso9660ImageWriter.LayoutPlan).
type IsoLayoutPlan struct {
	sourceDirectory         string
	volumeSectors           uint32
	fileCount               int
	directoryCount          int
	fileBytes               int64
	dataEndLbaExclusive     uint32
	metadataEndLbaExclusive uint32
	fileDataStartLba        uint32
	filesAtOuterRing        bool

	// Internal writer state (Iso9660ImageWriter.LayoutPlan internals).
	root               *isoNode
	directories        []*isoNode
	layout             isoLayout
	primaryVolumeLabel string
	jolietVolumeLabel  string
	timestamp          time.Time
}

func (p *IsoLayoutPlan) SourceDirectory() string         { return p.sourceDirectory }
func (p *IsoLayoutPlan) VolumeSectors() uint32           { return p.volumeSectors }
func (p *IsoLayoutPlan) FileCount() int                  { return p.fileCount }
func (p *IsoLayoutPlan) DirectoryCount() int             { return p.directoryCount }
func (p *IsoLayoutPlan) FileBytes() int64                { return p.fileBytes }
func (p *IsoLayoutPlan) DataEndLbaExclusive() uint32     { return p.dataEndLbaExclusive }
func (p *IsoLayoutPlan) MetadataEndLbaExclusive() uint32 { return p.metadataEndLbaExclusive }
func (p *IsoLayoutPlan) FileDataStartLba() uint32        { return p.fileDataStartLba }
func (p *IsoLayoutPlan) FilesAtOuterRing() bool          { return p.filesAtOuterRing }

// UdfLayoutPlan is a planned UDF 2.01 volume (UdfImageWriter.LayoutPlan).
type UdfLayoutPlan struct {
	sourceDirectory        string
	volumeSectors          uint32
	volumeLabel            string
	timestamp              time.Time
	fileCount              int
	directoryCount         int
	fileBytes              int64
	dataEndLbaExclusive    uint32
	drawingStartLba        uint32
	drawingEndLbaExclusive uint32
	filesAtOuterRing       bool
	partitionStartLba      uint32

	// Internal writer state (UdfImageWriter.LayoutPlan.Nodes).
	nodes []*udfNode
}

func (p *UdfLayoutPlan) SourceDirectory() string        { return p.sourceDirectory }
func (p *UdfLayoutPlan) VolumeSectors() uint32          { return p.volumeSectors }
func (p *UdfLayoutPlan) VolumeLabel() string            { return p.volumeLabel }
func (p *UdfLayoutPlan) Timestamp() time.Time           { return p.timestamp }
func (p *UdfLayoutPlan) FileCount() int                 { return p.fileCount }
func (p *UdfLayoutPlan) DirectoryCount() int            { return p.directoryCount }
func (p *UdfLayoutPlan) FileBytes() int64               { return p.fileBytes }
func (p *UdfLayoutPlan) DataEndLbaExclusive() uint32    { return p.dataEndLbaExclusive }
func (p *UdfLayoutPlan) DrawingStartLba() uint32        { return p.drawingStartLba }
func (p *UdfLayoutPlan) DrawingEndLbaExclusive() uint32 { return p.drawingEndLbaExclusive }
func (p *UdfLayoutPlan) FilesAtOuterRing() bool         { return p.filesAtOuterRing }
func (p *UdfLayoutPlan) PartitionStartLba() uint32      { return p.partitionStartLba }

// ReserveVdsLba is the backup descriptor sequence start.
func (p *UdfLayoutPlan) ReserveVdsLba() uint32 { return p.volumeSectors - 256 }

// PartitionSectors is the physical partition length.
func (p *UdfLayoutPlan) PartitionSectors() uint32 { return p.dataEndLbaExclusive - p.partitionStartLba }
