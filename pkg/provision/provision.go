package provision

import "github.com/apprenda/kismatic/pkg/install"

// Provisioner is responsible for creating and destroying infrastructure for
// a given cluster.
type Provisioner interface {
	Provision(install.Plan) (*install.Plan, error)
	Destroy(provider, clusterName string) error
}

//An aggregate of different tfNodes (different fields, the same nodes)
//NOTE: these are organized a little differently than a traditional node group
//due to limitations of terraform. A tfNodeGroup organizes each field into
//parallel slices as opposed to a single slice with nodes containing the same data.
type tfNodeGroup struct {
	IPs         []string
	InternalIPs []string
	Hosts       []string
}

// For deserializing terraform output
type tfOutputVar struct {
	Sensitive  bool     `json:"sensitive"`
	OutputType string   `json:"type"`
	Value      []string `json:"value"`
}
