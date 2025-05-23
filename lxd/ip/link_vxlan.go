package ip

// Vxlan represents arguments for link of type vxlan.
type Vxlan struct {
	Link
	VxlanID string
	DevName string
	Local   string
	Remote  string
	Group   string
	DstPort string
	TTL     string
	FanMap  string
}

// additionalArgs generates vxlan specific arguments.
func (vxlan *Vxlan) additionalArgs() []string {
	args := []string{}
	args = append(args, "id", vxlan.VxlanID)
	if vxlan.DevName != "" {
		args = append(args, "dev", vxlan.DevName)
	}

	if vxlan.Group != "" {
		args = append(args, "group", vxlan.Group)
	}

	if vxlan.Remote != "" {
		args = append(args, "remote", vxlan.Remote)
	}

	if vxlan.Local != "" {
		args = append(args, "local", vxlan.Local)
	}

	if vxlan.TTL != "" {
		args = append(args, "ttl", vxlan.TTL)
	}

	if vxlan.DstPort != "" {
		args = append(args, "dstport", vxlan.DstPort)
	}

	if vxlan.FanMap != "" {
		args = append(args, "fan-map", vxlan.FanMap)
	}

	return args
}

// Add adds new virtual link.
func (vxlan *Vxlan) Add() error {
	return vxlan.add("vxlan", vxlan.additionalArgs())
}
