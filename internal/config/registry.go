package config

// OptionInfo is the subset of rclone's option metadata that validation needs.
type OptionInfo struct {
	Name      string
	Required  bool
	Sensitive bool // rclone's Sensitive flag: redacted in bug reports; includes identifiers
	Password  bool // rclone's IsPassword flag: a real credential, never allowed in the file
	Advanced  bool
	Default   string
	Type      string
	Provider  string // rclone provider filter, e.g. "AWS" or "!AWS,Alibaba"; "" = all
	Examples  []string
	Exclusive bool   // value must be one of Examples
	Help      string // first paragraph of rclone's help text
	// Choices carries the examples with their descriptions and provider
	// filters, for user interfaces.
	Choices []Choice
}

// Choice is one documented example value of an option.
type Choice struct {
	Value    string `json:"value"`
	Help     string `json:"help"`
	Provider string `json:"provider,omitempty"`
}

// OptionSet describes the options of one backend or of the VFS layer.
type OptionSet interface {
	// Lookup returns metadata for an option by its rclone name.
	Lookup(name string) (OptionInfo, bool)
	// Check parses value the way rclone would and returns an error if it does
	// not fit the option's type.
	Check(name, value string) error
	// Options lists all options.
	Options() []OptionInfo
}

// Registry gives validation access to option metadata without importing rclone.
type Registry interface {
	// Backend returns the option set for a backend type such as "s3".
	Backend(typ string) (OptionSet, bool)
	// VFS returns the option set of the VFS layer.
	VFS() OptionSet
	// NeedsBucket reports whether the backend's root lists buckets rather
	// than files (S3 and other object stores), so a mount's path must name
	// one.
	NeedsBucket(typ string) bool
	// Option looks an option up across every backend, for accounts, which
	// are not tied to one: whether some backend has it, with the strictest
	// flags any of them sets (Sensitive, Password).
	Option(name string) (OptionInfo, bool)
}
