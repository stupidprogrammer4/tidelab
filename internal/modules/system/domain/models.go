package domain

// Config contains the local runtime settings shared by entrypoints.
type Config struct {
	DataDir    string `json:"data_dir"`
	ListenAddr string `json:"listen_addr"`
}

type Version struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Health struct {
	Status string `json:"status"`
}
