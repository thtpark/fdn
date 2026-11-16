package domain

const SoftwareManifestVersion = 1
const MaxSoftwareBundleBytes int64 = 256 << 20

type SoftwareProduct struct {
	ID           string            `json:"id"`
	ProjectID    string            `json:"project_id"`
	RepositoryID string            `json:"repository_id,omitempty"`
	Name         string            `json:"name"`
	Kind         string            `json:"kind"`
	Provenance   string            `json:"provenance"`
	RecipePath   string            `json:"recipe_path,omitempty"`
	RecipePaths  map[string]string `json:"recipe_paths,omitempty"`
	Capabilities []string          `json:"capabilities"`
	License      string            `json:"license"`
	Revision     int64             `json:"revision"`
	CreatedBy    string            `json:"created_by"`
	CreatedAt    int64             `json:"created_at"`
}

type SoftwareProductRequest struct {
	Command
	ProjectID    string            `json:"project_id"`
	RepositoryID string            `json:"repository_id,omitempty"`
	Name         string            `json:"name"`
	Kind         string            `json:"kind"`
	Provenance   string            `json:"provenance"`
	RecipePath   string            `json:"recipe_path,omitempty"`
	RecipePaths  map[string]string `json:"recipe_paths,omitempty"`
	Capabilities []string          `json:"capabilities"`
	License      string            `json:"license"`
	Reason       string            `json:"reason"`
}

type SoftwareDependency struct {
	ProductID string `json:"product_id"`
	ReleaseID string `json:"release_id"`
	Platform  string `json:"platform"`
	SHA256    string `json:"sha256"`
	Purpose   string `json:"purpose"`
}

type BuildStep struct {
	Args []string `json:"args"`
}

type BuildOutput struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
}

type BuildRecipe struct {
	FormatVersion  int                  `json:"format_version"`
	Version        string               `json:"version"`
	Platform       string               `json:"platform"`
	TimeoutSeconds int                  `json:"timeout_seconds"`
	Steps          []BuildStep          `json:"steps"`
	Outputs        []BuildOutput        `json:"outputs"`
	Dependencies   []SoftwareDependency `json:"dependencies"`
	Profile        string               `json:"profile"`
}

type SoftwareBuild struct {
	ID             string            `json:"id"`
	ProductID      string            `json:"product_id"`
	TaskID         string            `json:"task_id"`
	SpecGeneration int64             `json:"spec_generation"`
	Source         TaskSource        `json:"source"`
	Recipe         BuildRecipe       `json:"recipe"`
	RecipeSHA256   string            `json:"recipe_sha256"`
	State          string            `json:"state"`
	BuilderID      string            `json:"builder_id,omitempty"`
	RunID          string            `json:"run_id,omitempty"`
	ArtifactID     string            `json:"artifact_id,omitempty"`
	Payloads       []SoftwarePayload `json:"payloads,omitempty"`
	CreatedBy      string            `json:"created_by"`
	CreatedAt      int64             `json:"created_at"`
}

type SoftwareBuildRequest struct {
	Command
	ProductID string `json:"product_id"`
	TaskID    string `json:"task_id"`
	Platform  string `json:"platform"`
}

type SoftwareBuildResult struct {
	Command
	RunID        string `json:"run_id"`
	BundleFileID string `json:"bundle_file_id"`
	Toolchain    string `json:"toolchain"`
	Summary      string `json:"summary"`
}

type SoftwarePayload struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

type SoftwarePlatform struct {
	Platform     string               `json:"platform"`
	ArtifactID   string               `json:"artifact_id"`
	SHA256       string               `json:"sha256"`
	Size         int64                `json:"size"`
	Profile      string               `json:"profile"`
	Payloads     []SoftwarePayload    `json:"payloads"`
	Dependencies []SoftwareDependency `json:"dependencies"`
}

type SoftwareManifest struct {
	FormatVersion int                `json:"format_version"`
	InstanceID    string             `json:"instance_id"`
	ReleaseID     string             `json:"release_id"`
	ProductID     string             `json:"product_id"`
	ProjectID     string             `json:"project_id"`
	Kind          string             `json:"kind"`
	Version       string             `json:"version"`
	Revision      int64              `json:"revision"`
	Source        TaskSource         `json:"source"`
	Platforms     []SoftwarePlatform `json:"platforms"`
	CreatedAt     int64              `json:"created_at"`
}

type SoftwareRelease struct {
	ID        string           `json:"id"`
	Manifest  SoftwareManifest `json:"manifest"`
	KeyID     string           `json:"key_id"`
	Signature string           `json:"signature"`
	BuildIDs  []string         `json:"build_ids"`
	CreatedBy string           `json:"created_by"`
	Revoked   bool             `json:"revoked"`
}

type SoftwareReleaseRequest struct {
	Command
	ProductID string   `json:"product_id"`
	BuildIDs  []string `json:"build_ids"`
	Reason    string   `json:"reason"`
}

type SoftwareReleaseChange struct {
	Command
	Reason string `json:"reason"`
}

type SoftwareObservation struct {
	ProductID string `json:"product_id"`
	State     string `json:"state"`
	Version   string `json:"version,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Manager   string `json:"manager"`
}

type SoftwareObservationRequest struct {
	Command
	Observation SoftwareObservation `json:"observation"`
}

type SoftwareInventory struct {
	BindingID   string              `json:"binding_id"`
	AgentID     string              `json:"agent_id"`
	Generation  int64               `json:"generation"`
	Observation SoftwareObservation `json:"observation"`
	ReleaseID   string              `json:"release_id,omitempty"`
	ReceiptID   string              `json:"receipt_id,omitempty"`
	ObservedAt  int64               `json:"observed_at"`
}

type SoftwareConsumer struct {
	ID         string             `json:"id"`
	ProjectID  string             `json:"project_id"`
	Dependency SoftwareDependency `json:"dependency"`
	Revision   int64              `json:"revision"`
	UpdatedBy  string             `json:"updated_by"`
	UpdatedAt  int64              `json:"updated_at"`
}

type SoftwareConsumerRequest struct {
	Command
	ProjectID  string             `json:"project_id"`
	Dependency SoftwareDependency `json:"dependency"`
	Reason     string             `json:"reason"`
}

type SoftwareChallenge struct {
	ID             string `json:"id"`
	Nonce          string `json:"nonce"`
	InstanceID     string `json:"instance_id"`
	TargetID       string `json:"target_id"`
	BindingID      string `json:"binding_id"`
	Generation     int64  `json:"generation"`
	ProductID      string `json:"product_id"`
	Platform       string `json:"platform"`
	State          string `json:"state"`
	CurrentSHA256  string `json:"current_sha256,omitempty"`
	CurrentVersion string `json:"current_version,omitempty"`
	CurrentRelease string `json:"current_release,omitempty"`
	PolicySHA256   string `json:"policy_sha256"`
	ExpiresAt      int64  `json:"expires_at"`
}

type SoftwareProposal struct {
	Challenge       SoftwareChallenge `json:"challenge"`
	Release         SoftwareRelease   `json:"release"`
	BackupRecipient string            `json:"backup_recipient"`
	Reason          string            `json:"reason"`
}

type SoftwarePermit struct {
	ID              string           `json:"id"`
	Proposal        SoftwareProposal `json:"proposal"`
	ActorID         string           `json:"actor_id"`
	ActorBindingID  string           `json:"actor_binding_id"`
	ActorGeneration int64            `json:"actor_generation"`
	IssuedAt        int64            `json:"issued_at"`
	ExpiresAt       int64            `json:"expires_at"`
	Signature       string           `json:"signature"`
}

type SoftwareAuthorizationRequest struct {
	Command
	PreflightID   string           `json:"preflight_id"`
	MaintenanceID string           `json:"maintenance_id,omitempty"`
	Proposal      SoftwareProposal `json:"proposal"`
}

type SoftwarePreflightRequest struct {
	Command
	TargetID  string                `json:"target_id"`
	BindingID string                `json:"binding_id"`
	ProductID string                `json:"product_id"`
	Native    SoftwareNativeRequest `json:"native"`
}

type SoftwarePreflight struct {
	ID               string                   `json:"id"`
	FounderID        string                   `json:"founder_id"`
	FounderBinding   string                   `json:"founder_binding"`
	Generation       int64                    `json:"generation"`
	TargetGeneration int64                    `json:"target_generation"`
	Request          SoftwarePreflightRequest `json:"request"`
	State            string                   `json:"state"`
	ExpiresAt        int64                    `json:"expires_at"`
	Result           *SoftwareNativeReply     `json:"result,omitempty"`
}

type SoftwarePreflightReport struct {
	Command
	ID     string              `json:"id"`
	Result SoftwareNativeReply `json:"result"`
}

type SoftwareResult struct {
	ResumeSafe   bool   `json:"resume_safe"`
	PermitID     string `json:"permit_id"`
	PermitSHA256 string `json:"permit_sha256"`
	ProductID    string `json:"product_id"`
	ReleaseID    string `json:"release_id"`
	ArtifactID   string `json:"artifact_id"`
	SHA256       string `json:"sha256"`
	BackupSHA256 string `json:"backup_sha256"`
	State        string `json:"state"`
	Code         string `json:"code,omitempty"`
}

type SoftwareResultRequest struct {
	Command
	Result SoftwareResult `json:"result"`
}

type SoftwareDeployment struct {
	MaintenanceID string          `json:"maintenance_id,omitempty"`
	ID            string          `json:"id"`
	Permit        SoftwarePermit  `json:"permit"`
	State         string          `json:"state"`
	Result        *SoftwareResult `json:"result,omitempty"`
	CreatedAt     int64           `json:"created_at"`
}

type SoftwareMaintenance struct {
	ID             string              `json:"id"`
	FounderID      string              `json:"founder_id"`
	FounderBinding string              `json:"founder_binding"`
	Generation     int64               `json:"generation"`
	PreflightIDs   []string            `json:"preflight_ids"`
	DeploymentIDs  []string            `json:"deployment_ids"`
	PausedModes    []UpgradePausedMode `json:"paused_modes"`
	State          string              `json:"state"`
	Reason         string              `json:"reason,omitempty"`
}

type SoftwareMaintenanceRequest struct {
	Command
	ID           string   `json:"id"`
	PreflightIDs []string `json:"preflight_ids"`
	Reason       string   `json:"reason"`
}

type SoftwareNativeRequest struct {
	Action    string          `json:"action"`
	ID        string          `json:"id"`
	ProductID string          `json:"product_id,omitempty"`
	Permit    *SoftwarePermit `json:"permit,omitempty"`
}

type SoftwareNativeReply struct {
	Challenge *SoftwareChallenge `json:"challenge,omitempty"`
	Result    *SoftwareResult    `json:"result,omitempty"`
	State     string             `json:"state"`
	Error     *Fault             `json:"error,omitempty"`
}

type SoftwareMailboxRequest struct {
	Action          string                   `json:"action"`
	ArtifactID      string                   `json:"artifact_id,omitempty"`
	Offset          int64                    `json:"offset,omitempty"`
	PreflightReport *SoftwarePreflightReport `json:"preflight_report,omitempty"`
	Result          *SoftwareResultRequest   `json:"result,omitempty"`
	Lease           *UpgradeLeaseRequest     `json:"lease,omitempty"`
}

type SoftwareMailboxReply struct {
	Preflights  []SoftwarePreflight  `json:"preflights,omitempty"`
	Deployments []SoftwareDeployment `json:"deployments,omitempty"`
	Preflight   *SoftwarePreflight   `json:"preflight,omitempty"`
	Deployment  *SoftwareDeployment  `json:"deployment,omitempty"`
	Lease       *UpgradeLease        `json:"lease,omitempty"`
	Artifact    *DeliveryArtifact    `json:"artifact,omitempty"`
	Chunk       *FileChunk           `json:"chunk,omitempty"`
	Error       *Fault               `json:"error,omitempty"`
}
