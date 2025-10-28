package domain

type NIC struct {
	MacAddress      string `json:"mac_address,omitempty"`
	PrimaryIP       string `json:"primary_ip,omitempty"`
	SubnetCIDRBlock string `json:"subnet_cidr_block,omitempty"`
}

type DriveInfo struct {
	SerialId   string `json:"serial_id"`
	DevicePath string `json:"block_device"`
	Partition  string `json:"partition"`
	IsSigned   bool   `json:"is_signed"`           // Means drive is signed by Weka
	WekaGuid   string `json:"weka_guid,omitempty"` // Only populated if drive is signed
}
