package gobox

// Mode is selected by protected host policy, never by an extension.
type Mode string

const (
	Off    Mode = "off"
	Auto   Mode = "auto"
	Strict Mode = "strict"
)

// Feature identifies a sandbox protection that a backend can report.
type Feature string

const (
	PrivilegeRemoval    Feature = "privilege-removal"
	Environment         Feature = "environment"
	HandleInheritance   Feature = "handle-inheritance"
	WritableStorage     Feature = "writable-storage"
	FilesystemAccess    Feature = "filesystem-access"
	ExactFilesystemView Feature = "exact-filesystem-view"
	PrivateNetwork      Feature = "private-network"
	ProcessIsolation    Feature = "process-isolation"
	ProcessTree         Feature = "process-tree"
	ResourceLimits      Feature = "resource-limits"
)

type FeatureState string

const (
	Applied     FeatureState = "applied"
	OmittedAuto FeatureState = "omitted-auto"
	ExcludedOS  FeatureState = "excluded-os"
	Disabled    FeatureState = "off"
)

// FeatureResult records how one selected protection was applied.
type FeatureResult struct {
	Feature Feature
	State   FeatureState
	Reason  string
}

// State is the lifecycle state of a Sandbox.
type State string

const (
	Preparing State = "preparing"
	Ready     State = "ready"
	Closing   State = "closing"
	Closed    State = "closed"
	Failed    State = "failed"
)

// Status contains no host paths, process handles, or credentials.
// Implementations must return copies of slices and maps.
type Status struct {
	Owner             Owner
	Template          TemplateID
	Generation        uint64
	Mode              Mode
	Profile           string
	EffectiveIdentity string
	Features          []FeatureResult
	Warnings          []string
	StrictSatisfied   bool
	FullBoundary      bool
	State             State
	Limits            map[string]int64
	Cleanup           string
}

// ServiceStatus is a bounded, host-path-free preflight summary. Template plans
// do not imply that an instance or child process is running.
type ServiceStatus struct {
	Mode              Mode
	Platform          string
	EffectiveIdentity string
	Templates         map[TemplateID]Status
	Limits            map[string]int64
	ActiveOwners      int64
	OrphanedOwners    int64
	QuarantinedOwners int64
}

// Clone returns a deep copy of mutable ServiceStatus fields.
func (s ServiceStatus) Clone() ServiceStatus {
	if s.Templates != nil {
		templates := make(map[TemplateID]Status, len(s.Templates))
		for id, status := range s.Templates {
			templates[id] = status.Clone()
		}
		s.Templates = templates
	}
	if s.Limits != nil {
		limits := make(map[string]int64, len(s.Limits))
		for name, value := range s.Limits {
			limits[name] = value
		}
		s.Limits = limits
	}
	return s
}

// StatusReporter is an optional service preflight interface.
type StatusReporter interface {
	ServiceStatus() ServiceStatus
}

// Clone returns a deep copy of mutable Status fields.
func (s Status) Clone() Status {
	s.Features = append([]FeatureResult(nil), s.Features...)
	s.Warnings = append([]string(nil), s.Warnings...)
	if s.Limits != nil {
		limits := make(map[string]int64, len(s.Limits))
		for name, value := range s.Limits {
			limits[name] = value
		}
		s.Limits = limits
	}
	return s
}
