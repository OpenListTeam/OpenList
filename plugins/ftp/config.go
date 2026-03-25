package ftp

type FTP struct {
	Listen                  string `json:"listen" env:"LISTEN"`
	FindPasvPortAttempts    int    `json:"find_pasv_port_attempts" env:"FIND_PASV_PORT_ATTEMPTS"`
	ActiveTransferPortNon20 bool   `json:"active_transfer_port_non_20" env:"ACTIVE_TRANSFER_PORT_NON_20"`
	IdleTimeout             int    `json:"idle_timeout" env:"IDLE_TIMEOUT"`
	ConnectionTimeout       int    `json:"connection_timeout" env:"CONNECTION_TIMEOUT"`
	DisableActiveMode       bool   `json:"disable_active_mode" env:"DISABLE_ACTIVE_MODE"`
	DefaultTransferBinary   bool   `json:"default_transfer_binary" env:"DEFAULT_TRANSFER_BINARY"`
	EnableActiveConnIPCheck bool   `json:"enable_active_conn_ip_check" env:"ENABLE_ACTIVE_CONN_IP_CHECK"`
	EnablePasvConnIPCheck   bool   `json:"enable_pasv_conn_ip_check" env:"ENABLE_PASV_CONN_IP_CHECK"`
}
