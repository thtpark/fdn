package domain

const MaxGitBundleBytes int64 = 64 << 20
const MaxGitExpandedBytes int64 = 256 << 20
const MaxGitObjects = 65536
const MaxRepositoryRefs = 128

type GitRef struct {
	Name       string `json:"name"`
	Commit     string `json:"commit"`
	ArtifactID string `json:"artifact_id"`
}

type HostedRepository struct {
	ID            string   `json:"id"`
	ProjectID     string   `json:"project_id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	DefaultBranch string   `json:"default_branch"`
	Public        bool     `json:"public"`
	Revision      int64    `json:"revision"`
	CreatedBy     string   `json:"created_by"`
	CreatedAt     int64    `json:"created_at"`
	Refs          []GitRef `json:"refs"`
}

type RepositoryRequest struct {
	Command
	ProjectID     string `json:"project_id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	Public        bool   `json:"public"`
	BundleFileID  string `json:"bundle_file_id,omitempty"`
}

type GitRefUpdate struct {
	Name string `json:"name"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

type RepositoryPush struct {
	Command
	TaskID       string         `json:"task_id"`
	RunID        string         `json:"run_id"`
	BundleFileID string         `json:"bundle_file_id"`
	Updates      []GitRefUpdate `json:"updates"`
}

type RepositoryMerge struct {
	Command
	TaskID           string `json:"task_id"`
	RunID            string `json:"run_id"`
	SpecGeneration   int64  `json:"spec_generation"`
	ResultGeneration int64  `json:"result_generation"`
	SourceRef        string `json:"source_ref"`
	SourceCommit     string `json:"source_commit"`
	BaseCommit       string `json:"base_commit"`
	Commit           string `json:"commit"`
	BundleFileID     string `json:"bundle_file_id"`
	Reason           string `json:"reason"`
}

type TaskSource struct {
	RepositoryID string `json:"repository_id"`
	Commit       string `json:"commit"`
	Ref          string `json:"ref,omitempty"`
	ProductID    string `json:"product_id,omitempty"`
}

type RepositoryFile struct {
	RepositoryID string `json:"repository_id"`
	Commit       string `json:"commit"`
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	Data         []byte `json:"data"`
}

type DeliveryArtifact struct {
	ID           string `json:"id"`
	RepositoryID string `json:"repository_id,omitempty"`
	FileID       string `json:"file_id"`
	ProjectID    string `json:"project_id"`
	OwnerID      string `json:"owner_id"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	CreatedAt    int64  `json:"created_at"`
}
