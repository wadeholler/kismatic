package provision

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/apprenda/kismatic/pkg/install"
	"github.com/apprenda/kismatic/pkg/ssh"
	yaml "gopkg.in/yaml.v2"
)

const providerDescriptorFilename = "provider.yaml"

// The AnyTerraform provisioner uses Terraform to provision infrastructure using
// providers that adhere to the KET provisioner spec.
type AnyTerraform struct {
	KismaticVersion string
	ProvidersDir    string
	StateDir        string
	Output          io.Writer
	BinaryPath      string
}

type provider struct {
	Description          string            `yaml:"description"`
	EnvironmentVariables map[string]string `yaml:"environmentVariables"`
}

type ketVars struct {
	KismaticVersion   string `json:"kismatic_version"`
	PrivateSSHKeyPath string `json:"private_ssh_key_path"`
	PublicSSHKeyPath  string `json:"public_ssh_key_path"`
	ClusterName       string `json:"cluster_name"`
	MasterCount       int    `json:"master_count"`
	EtcdCount         int    `json:"etcd_count"`
	WorkerCount       int    `json:"worker_count"`
	IngressCount      int    `json:"ingress_count"`
	StorageCount      int    `json:"storage_count"`
}

func (at AnyTerraform) GetExpectedEnvVars(providerName string) (map[string]string, error) {
	providerDir := filepath.Join(at.ProvidersDir, providerName)
	if _, err := os.Stat(providerDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("provider %q is not supported", providerName)
	}

	// Read the provider configuration
	var p provider
	providerDescriptorFile := filepath.Join(providerDir, providerDescriptorFilename)
	b, err := ioutil.ReadFile(providerDescriptorFile)
	if err != nil {
		return nil, fmt.Errorf("could not read provider descriptor: %v", err)
	}
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("could not unmarshal provider descriptor from %q: %v", providerDescriptorFile, err)
	}
	return p.EnvironmentVariables, nil
}

func (at AnyTerraform) Provision(plan install.Plan) (*install.Plan, error) {
	providerName := plan.Provisioner.Provider
	providerDir := filepath.Join(at.ProvidersDir, providerName)
	if _, err := os.Stat(providerDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("provider %q is not supported", providerName)
	}

	// Read the provider configuration
	var p provider
	providerDescriptorFile := filepath.Join(providerDir, providerDescriptorFilename)
	b, err := ioutil.ReadFile(providerDescriptorFile)
	if err != nil {
		return nil, fmt.Errorf("could not read provider descriptor: %v", err)
	}
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("could not unmarshal provider descriptor from %q: %v", providerDescriptorFile, err)
	}

	// Create directory for keeping cluster state
	clusterStateDir := filepath.Join(at.StateDir, plan.Cluster.Name)
	if err := os.MkdirAll(clusterStateDir, 0700); err != nil {
		return nil, fmt.Errorf("error creating directory to keep cluster state: %v", err)
	}

	pubKeyPath := filepath.Join(clusterStateDir, fmt.Sprintf("%s-ssh.pub", plan.Cluster.Name))
	privKeyPath := filepath.Join(clusterStateDir, fmt.Sprintf("%s-ssh.pem", plan.Cluster.Name))

	var privKeyExists, pubKeyExists bool
	if _, err := os.Stat(pubKeyPath); err == nil {
		pubKeyExists = true
	}
	if _, err := os.Stat(privKeyPath); err == nil {
		privKeyExists = true
	}

	if pubKeyExists != privKeyExists {
		if !privKeyExists {
			return nil, fmt.Errorf("found an existing public key at %s, but did not find the corresponding private key at %s. The corresponding key must be recovered if possible. Otherwise, the existing key must be deleted", pubKeyPath, privKeyPath)
		}
		return nil, fmt.Errorf("found an existing private key at %s, but did not find the corresponding public key at %s. The corresponding key must be recovered if possible. Otherwise, the existing key must be deleted", privKeyPath, pubKeyPath)
	}

	if !privKeyExists && !pubKeyExists {
		if err := ssh.NewKeyPair(pubKeyPath, privKeyPath); err != nil {
			return nil, fmt.Errorf("error generating SSH key pair: %v", err)
		}
	}
	plan.Cluster.SSH.Key = privKeyPath

	// Write out the KET terraform variables
	data := ketVars{
		KismaticVersion:   at.KismaticVersion,
		ClusterName:       plan.Cluster.Name,
		MasterCount:       plan.Master.ExpectedCount,
		EtcdCount:         plan.Etcd.ExpectedCount,
		WorkerCount:       plan.Worker.ExpectedCount,
		IngressCount:      plan.Ingress.ExpectedCount,
		StorageCount:      plan.Storage.ExpectedCount,
		PrivateSSHKeyPath: privKeyPath,
		PublicSSHKeyPath:  pubKeyPath,
	}
	b, err = json.MarshalIndent(data, "", "  ")
	if err != nil {
		return nil, err
	}
	err = ioutil.WriteFile(filepath.Join(clusterStateDir, "terraform.tfvars"), b, 0644)
	if err != nil {
		return nil, fmt.Errorf("error writing terraform variables: %v", err)
	}

	// Write out provider options as terraform variables, filtering out the ones that should be exposed as env vars
	varsToWriteOut := map[string]string{}
	for k, v := range plan.Provisioner.Options {
		var shouldBeEnvVar bool
		for envVarKey := range p.EnvironmentVariables {
			if k == envVarKey {
				shouldBeEnvVar = true
			}
		}
		if shouldBeEnvVar {
			continue
		}
		varsToWriteOut[k] = v
	}

	b, err = json.MarshalIndent(varsToWriteOut, "", "  ")
	if err != nil {
		return nil, err
	}
	err = ioutil.WriteFile(filepath.Join(clusterStateDir, "provider.auto.tfvars"), b, 0644)
	if err != nil {
		return nil, fmt.Errorf("error writing tfvars file for provider-specific options")
	}

	// Setup the environment for all Terraform commands.
	cmdEnv, err := buildCommandEnv(plan.Provisioner.Options, p.EnvironmentVariables)
	if err != nil {
		return nil, fmt.Errorf("could not get environment variables for terraform commands: %v", err)
	}
	cmdDir := clusterStateDir

	// Terraform init
	initCmd := exec.Command(at.BinaryPath, "init", providerDir)
	initCmd.Env = cmdEnv
	initCmd.Dir = cmdDir
	initCmd.Stdout = at.Output
	initCmd.Stderr = at.Output
	if err := initCmd.Run(); err != nil {
		return nil, fmt.Errorf("Error initializing terraform: %s", err)
	}

	// Terraform plan
	planCmd := exec.Command(at.BinaryPath, "plan", fmt.Sprintf("-out=%s", plan.Cluster.Name), providerDir)
	planCmd.Env = cmdEnv
	planCmd.Dir = cmdDir
	planCmd.Stdout = at.Output
	planCmd.Stderr = at.Output

	if err := planCmd.Run(); err != nil {
		return nil, fmt.Errorf("Error running terraform plan: %s", err)
	}

	// Terraform apply
	applyCmd := exec.Command(at.BinaryPath, "apply", "-input=false", plan.Cluster.Name)
	applyCmd.Stdout = at.Output
	applyCmd.Stderr = at.Output
	applyCmd.Env = cmdEnv
	applyCmd.Dir = cmdDir
	if err := applyCmd.Run(); err != nil {
		return nil, fmt.Errorf("Error running terraform apply: %s", err)
	}

	// Update plan
	provisionedPlan, err := at.buildPopulatedPlan(plan)
	if err != nil {
		return nil, err
	}
	return provisionedPlan, nil
}

func (at AnyTerraform) Destroy(plan install.Plan) error {
	return nil
}

// Returns a slice of env vars that are to be used for all terraform commands.
// The slice is the intersection of the options that were set for the provider
// and the list of expected env vars.
func buildCommandEnv(providerOptions map[string]string, expectedEnvVars map[string]string) ([]string, error) {
	env := os.Environ()
	env = append(env, "TF_IN_AUTOMATION=True")
	for optionName, envVarName := range expectedEnvVars {
		var found bool
		for k, v := range providerOptions {
			if k == optionName {
				env = append(env, fmt.Sprintf("%s=%s", envVarName, v))
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("provider option %q is required and it was not specified", optionName)
		}
	}
	return env, nil
}

func (at AnyTerraform) getLoadBalancer(clusterName, lbName string) (string, error) {
	tfOutLB := fmt.Sprintf("%s_lb", lbName)
	cmdDir := filepath.Join(at.StateDir, clusterName)

	//load balancer
	tfCmdOutputLB := exec.Command(at.BinaryPath, "output", "-json", tfOutLB)
	tfCmdOutputLB.Dir = cmdDir
	stdoutStderrLB, err := tfCmdOutputLB.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("Error collecting terraform output: %s", stdoutStderrLB)
	}
	lbData := tfOutputVar{}
	if err := json.Unmarshal(stdoutStderrLB, &lbData); err != nil {
		return "", err
	}
	if len(lbData.Value) != 1 {
		return "", fmt.Errorf("Expect to get 1 load balancer, but got %d", len(lbData.Value))
	}
	return lbData.Value[0], nil
}

func (at AnyTerraform) getTerraformNodes(clusterName, role string) (*tfNodeGroup, error) {
	tfOutPubIPs := fmt.Sprintf("%s_pub_ips", role)
	tfOutPrivIPs := fmt.Sprintf("%s_priv_ips", role)
	tfOutHosts := fmt.Sprintf("%s_hosts", role)
	cmdDir := filepath.Join(at.StateDir, clusterName)

	nodes := &tfNodeGroup{}

	//Public IPs
	tfCmdOutputPub := exec.Command(at.BinaryPath, "output", "-json", tfOutPubIPs)
	tfCmdOutputPub.Dir = cmdDir
	stdoutStderrPub, err := tfCmdOutputPub.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("Error collecting terraform output: %s", stdoutStderrPub)
	}
	pubIPData := tfOutputVar{}
	if err := json.Unmarshal(stdoutStderrPub, &pubIPData); err != nil {
		return nil, err
	}
	nodes.IPs = pubIPData.Value

	//Private IPs
	tfCmdOutputPriv := exec.Command(at.BinaryPath, "output", "-json", tfOutPrivIPs)
	tfCmdOutputPriv.Dir = cmdDir
	stdoutStderrPriv, err := tfCmdOutputPriv.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("Error collecting terraform output: %s", stdoutStderrPriv)
	}
	privIPData := tfOutputVar{}
	if err := json.Unmarshal(stdoutStderrPriv, &privIPData); err != nil {
		return nil, err
	}
	nodes.InternalIPs = privIPData.Value

	//Hosts
	tfCmdOutputHost := exec.Command(at.BinaryPath, "output", "-json", tfOutHosts)
	tfCmdOutputHost.Dir = cmdDir
	stdoutStderrHost, err := tfCmdOutputHost.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("Error collecting terraform output: %s", stdoutStderrHost)
	}
	hostData := tfOutputVar{}
	if err := json.Unmarshal(stdoutStderrHost, &hostData); err != nil {
		return nil, err
	}
	nodes.Hosts = hostData.Value

	if len(nodes.IPs) != len(nodes.Hosts) {
		return nil, fmt.Errorf("Expected to get %d host names, but got %d", len(nodes.IPs), len(nodes.Hosts))
	}

	// Verify that we got the right number of internal IPs if we are using them
	if len(nodes.InternalIPs) != 0 && len(nodes.IPs) != len(nodes.InternalIPs) {
		return nil, fmt.Errorf("Expected to get %d internal IPs, but got %d", len(nodes.IPs), len(nodes.InternalIPs))
	}

	return nodes, nil
}

func (at AnyTerraform) getClusterStateDir(clusterName string) (string, error) {
	path, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(path, "terraform", "clusters", clusterName), nil
}

func nodeGroupFromSlices(ips, internalIPs, hosts []string) install.NodeGroup {
	ng := install.NodeGroup{}
	ng.ExpectedCount = len(ips)
	ng.Nodes = []install.Node{}
	for i := range ips {
		n := install.Node{
			IP:   ips[i],
			Host: hosts[i],
		}
		if len(internalIPs) != 0 {
			n.InternalIP = internalIPs[i]
		}
		ng.Nodes = append(ng.Nodes, n)
	}
	return ng
}

// updatePlan
func (at AnyTerraform) buildPopulatedPlan(plan install.Plan) (*install.Plan, error) {
	// Masters
	tfNodes, err := at.getTerraformNodes(plan.Cluster.Name, "master")
	if err != nil {
		return nil, err
	}
	masterNodes := nodeGroupFromSlices(tfNodes.IPs, tfNodes.InternalIPs, tfNodes.Hosts)
	mng := install.MasterNodeGroup{
		ExpectedCount: masterNodes.ExpectedCount,
		Nodes:         masterNodes.Nodes,
	}
	mlb, err := at.getLoadBalancer(plan.Cluster.Name, "master")
	if err != nil {
		return nil, err
	}
	mng.LoadBalancedFQDN = mlb
	mng.LoadBalancedShortName = mlb
	plan.Master = mng

	// Etcds
	tfNodes, err = at.getTerraformNodes(plan.Cluster.Name, "etcd")
	if err != nil {
		return nil, err
	}
	plan.Etcd = nodeGroupFromSlices(tfNodes.IPs, tfNodes.InternalIPs, tfNodes.Hosts)

	// Workers
	tfNodes, err = at.getTerraformNodes(plan.Cluster.Name, "worker")
	if err != nil {
		return nil, err
	}
	plan.Worker = nodeGroupFromSlices(tfNodes.IPs, tfNodes.InternalIPs, tfNodes.Hosts)

	// Ingress
	if plan.Ingress.ExpectedCount > 0 {
		tfNodes, err = at.getTerraformNodes(plan.Cluster.Name, "ingress")
		if err != nil {
			return nil, fmt.Errorf("error getting ingress node information: %v", err)
		}
		plan.Ingress = install.OptionalNodeGroup(nodeGroupFromSlices(tfNodes.IPs, tfNodes.InternalIPs, tfNodes.Hosts))
	}

	// Storage
	if plan.Storage.ExpectedCount > 0 {
		tfNodes, err = at.getTerraformNodes(plan.Cluster.Name, "storage")
		if err != nil {
			return nil, fmt.Errorf("error getting storage node information: %v", err)
		}
		plan.Storage = install.OptionalNodeGroup(nodeGroupFromSlices(tfNodes.IPs, tfNodes.InternalIPs, tfNodes.Hosts))
	}

	return &plan, nil
}
