package domain

type NIC struct {
	MacAddress      string `json:"mac_address,omitempty"`
	PrimaryIP       string `json:"primary_ip,omitempty"`
	SubnetCIDRBlock string `json:"subnet_cidr_block,omitempty"`
}
