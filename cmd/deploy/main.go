// Command deploy builds and ships the Bevcheck monolith to a single EC2 instance,
// and tears it back down. Pure Go on top of the AWS SDK v2 + x/crypto/ssh — no
// shell tooling, no AWS CLI, no S3.
//
// Subcommands:
//
//	build    cross-compile the monolith only
//	deploy   build + provision IAM/EC2 + upload + start + verify (default)
//	down     terminate the instance and delete SG/keypair/profile/role
//
// Credentials: read from the standard AWS env vars (AWS_ACCESS_KEY_ID,
// AWS_SECRET_ACCESS_KEY, AWS_REGION, optional AWS_SESSION_TOKEN) via the SDK's
// default config chain. Nothing is written to disk except the SSH keypair.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"golang.org/x/crypto/ssh"
)

type cfg struct {
	region       string
	instanceType string
	arch         string
	name         string
	port         string
	keyName      string
	keyPath      string
	roleName     string
	profileName  string
	sgName       string
}

func main() {
	c := loadCfg()

	cmd := "deploy"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	var err error
	switch cmd {
	case "build":
		err = buildBinary(c.arch)
	case "deploy":
		err = deploy(c)
	case "down":
		err = down(c)
	default:
		fmt.Fprintf(os.Stderr, "usage: deploy [build|deploy|down]\n")
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func loadCfg() cfg {
	home, _ := os.UserHomeDir()
	return cfg{
		region:       getenv("AWS_REGION", "us-east-1"),
		instanceType: getenv("INSTANCE_TYPE", "t4g.small"),
		arch:         getenv("ARCH", "arm64"),
		name:         getenv("NAME", "Bevcheck"),
		port:         getenv("PORT", "80"),
		keyName:      getenv("KEY_NAME", "Bevcheck-key"),
		keyPath:      getenv("KEY_PATH", filepath.Join(home, ".ssh", "Bevcheck.pem")),
		roleName:     getenv("ROLE_NAME", "Bevcheck-ec2-role"),
		profileName:  getenv("PROFILE_NAME", "Bevcheck-ec2-profile"),
		sgName:       getenv("SG_NAME", "Bevcheck-sg"),
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---------------------------------------------------------------------------
// build
// ---------------------------------------------------------------------------

func buildBinary(arch string) error {
	out := fmt.Sprintf("bin/Bevcheck-linux-%s", arch)
	log.Printf("building Bevcheck for linux/%s -> %s", arch, out)
	if err := os.MkdirAll("bin", 0o755); err != nil {
		return err
	}
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", out, "./cmd/Bevcheck")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ---------------------------------------------------------------------------
// deploy
// ---------------------------------------------------------------------------

func deploy(c cfg) error {
	ctx := context.Background()

	if err := buildBinary(c.arch); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	binary := fmt.Sprintf("bin/Bevcheck-linux-%s", c.arch)

	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(c.region))
	if err != nil {
		return fmt.Errorf("aws config: %w", err)
	}
	ec2c := ec2.NewFromConfig(awsCfg)
	iamc := iam.NewFromConfig(awsCfg)
	ssmc := ssm.NewFromConfig(awsCfg)

	if err := ensureRoleProfile(ctx, iamc, c); err != nil {
		return err
	}
	if err := ensureKeyPair(ctx, ec2c, c); err != nil {
		return err
	}
	sgID, err := ensureSecurityGroup(ctx, ec2c, c)
	if err != nil {
		return err
	}
	ami, err := resolveAMI(ctx, ssmc, ec2c, c.arch)
	if err != nil {
		return err
	}
	instanceID, err := ensureInstance(ctx, ec2c, c, ami, sgID)
	if err != nil {
		return err
	}
	fmt.Printf("instance: %s\n", instanceID)

	if err := waitInstanceReady(ctx, ec2c, instanceID); err != nil {
		return err
	}
	ip, err := publicIP(ctx, ec2c, instanceID)
	if err != nil {
		return err
	}
	fmt.Printf("public ip: %s\n", ip)

	if err := waitSSH(ip, c.keyPath); err != nil {
		return err
	}
	if err := installAndStart(ip, c.keyPath, binary, c.port, c.region); err != nil {
		return err
	}

	healthz := fmt.Sprintf("http://%s:%s/healthz", ip, c.port)
	if err := waitHealthy(healthz, 90*time.Second); err != nil {
		return err
	}
	fmt.Printf("Bevcheck live: http://%s:%s/\n", ip, c.port)
	return nil
}

// ---------------------------------------------------------------------------
// IAM
// ---------------------------------------------------------------------------

func ensureRoleProfile(ctx context.Context, iamc *iam.Client, c cfg) error {
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

	if _, err := iamc.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(c.roleName)}); err != nil {
		if _, err := iamc.CreateRole(ctx, &iam.CreateRoleInput{
			RoleName:                 aws.String(c.roleName),
			AssumeRolePolicyDocument: aws.String(trust),
		}); err != nil {
			return fmt.Errorf("create role: %w", err)
		}
		log.Printf("created role %s", c.roleName)
	}

	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["textract:DetectDocumentText"],"Resource":"*"}]}`
	if _, err := iamc.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(c.roleName),
		PolicyName:     aws.String("Bevcheck-textract"),
		PolicyDocument: aws.String(policy),
	}); err != nil {
		return fmt.Errorf("put role policy: %w", err)
	}

	if profile, err := iamc.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{
		InstanceProfileName: aws.String(c.profileName),
	}); err == nil {
		// Profile already provisioned — confirm the role is still attached
		// (deleting and recreating the role detaches it from the profile).
		for _, r := range profile.InstanceProfile.Roles {
			if aws.ToString(r.RoleName) == c.roleName {
				return nil
			}
		}
		if _, err := iamc.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{
			InstanceProfileName: aws.String(c.profileName),
			RoleName:            aws.String(c.roleName),
		}); err != nil {
			return fmt.Errorf("re-attach role to profile: %w", err)
		}
		log.Printf("re-attached role %s to profile %s", c.roleName, c.profileName)
		return nil
	}

	if _, err := iamc.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{
		InstanceProfileName: aws.String(c.profileName),
	}); err != nil {
		return fmt.Errorf("create instance profile: %w", err)
	}
	time.Sleep(5 * time.Second) // profile must exist before attaching the role
	if _, err := iamc.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{
		InstanceProfileName: aws.String(c.profileName),
		RoleName:            aws.String(c.roleName),
	}); err != nil {
		return fmt.Errorf("add role to profile: %w", err)
	}
	log.Printf("created instance profile %s", c.profileName)
	time.Sleep(8 * time.Second) // IAM propagation
	return nil
}

// ---------------------------------------------------------------------------
// keypair
// ---------------------------------------------------------------------------

func ensureKeyPair(ctx context.Context, ec2c *ec2.Client, c cfg) error {
	_, err := ec2c.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{KeyNames: []string{c.keyName}})
	if err == nil {
		if _, statErr := os.Stat(c.keyPath); statErr != nil {
			return fmt.Errorf("keypair %q exists in AWS but %s is missing", c.keyName, c.keyPath)
		}
		return nil
	}
	out, err := ec2c.CreateKeyPair(ctx, &ec2.CreateKeyPairInput{KeyName: aws.String(c.keyName)})
	if err != nil {
		return fmt.Errorf("create keypair: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(c.keyPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(c.keyPath, []byte(aws.ToString(out.KeyMaterial)), 0o400); err != nil {
		return err
	}
	log.Printf("created keypair %s -> %s", c.keyName, c.keyPath)
	return nil
}

// ---------------------------------------------------------------------------
// security group
// ---------------------------------------------------------------------------

func ensureSecurityGroup(ctx context.Context, ec2c *ec2.Client, c cfg) (string, error) {
	if id := sgIDByName(ctx, ec2c, c.sgName); id != "" {
		return id, nil
	}
	vpcID, subnetID, err := defaultVPCSubnet(ctx, ec2c)
	if err != nil {
		return "", err
	}
	out, err := ec2c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(c.sgName),
		Description: aws.String("Bevcheck prototype"),
		VpcId:       aws.String(vpcID),
	})
	if err != nil {
		return "", fmt.Errorf("create security group: %w", err)
	}
	sgID := aws.ToString(out.GroupId)

	port, _ := strconv.Atoi(c.port)
	sshCIDR, err := sshCIDRForIngress(myPublicIP())
	if err != nil {
		return "", err
	}
	_, err = ec2c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(sgID),
		IpPermissions: []ec2types.IpPermission{
			{
				IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22),
				IpRanges: []ec2types.IpRange{{CidrIp: aws.String(sshCIDR)}},
			},
			{
				IpProtocol: aws.String("tcp"), FromPort: aws.Int32(int32(port)), ToPort: aws.Int32(int32(port)),
				IpRanges: []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("authorize ingress: %w", err)
	}
	log.Printf("created security group %s (%s), subnet %s", c.sgName, sgID, subnetID)
	return sgID, nil
}

func sgIDByName(ctx context.Context, ec2c *ec2.Client, name string) string {
	out, err := ec2c.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{{Name: aws.String("group-name"), Values: []string{name}}},
	})
	if err == nil && len(out.SecurityGroups) > 0 {
		return aws.ToString(out.SecurityGroups[0].GroupId)
	}
	return ""
}

func defaultVPCSubnet(ctx context.Context, ec2c *ec2.Client) (string, string, error) {
	vpcs, err := ec2c.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{{Name: aws.String("isDefault"), Values: []string{"true"}}},
	})
	if err != nil || len(vpcs.Vpcs) == 0 {
		return "", "", fmt.Errorf("no default VPC found")
	}
	vpcID := aws.ToString(vpcs.Vpcs[0].VpcId)
	subs, err := ec2c.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}},
	})
	if err != nil || len(subs.Subnets) == 0 {
		return "", "", fmt.Errorf("no subnets in default VPC")
	}
	return vpcID, aws.ToString(subs.Subnets[0].SubnetId), nil
}

func myPublicIP() string {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("https://checkip.amazonaws.com")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	return strings.TrimSpace(string(b))
}

// sshCIDRForIngress returns the /32 CIDR for SSH ingress, failing closed when
// the public IP cannot be determined (never 0.0.0.0/0).
func sshCIDRForIngress(ip string) (string, error) {
	if strings.TrimSpace(ip) == "" {
		return "", fmt.Errorf("could not determine public IP; refusing to open SSH to 0.0.0.0/0")
	}
	return ip + "/32", nil
}

// ---------------------------------------------------------------------------
// AMI + instance
// ---------------------------------------------------------------------------

func resolveAMI(ctx context.Context, ssmc *ssm.Client, ec2c *ec2.Client, arch string) (string, error) {
	name := fmt.Sprintf("/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-%s", arch)
	if out, err := ssmc.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(name)}); err == nil {
		if out.Parameter != nil && out.Parameter.Value != nil {
			return aws.ToString(out.Parameter.Value), nil
		}
	}
	log.Printf("SSM AMI lookup failed; falling back to DescribeImages")
	out, err := ec2c.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{"amazon"},
		Filters: []ec2types.Filter{
			{Name: aws.String("name"), Values: []string{"al2023-ami-*-kernel-*-" + arch}},
			{Name: aws.String("state"), Values: []string{"available"}},
			{Name: aws.String("architecture"), Values: []string{arch}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("describe images: %w", err)
	}
	if len(out.Images) == 0 {
		return "", fmt.Errorf("no AL2023 %s AMI found", arch)
	}
	// latest by creation date (ISO-8601 sorts lexicographically)
	latest := out.Images[0]
	for _, img := range out.Images[1:] {
		if aws.ToString(img.CreationDate) > aws.ToString(latest.CreationDate) {
			latest = img
		}
	}
	return aws.ToString(latest.ImageId), nil
}

func ensureInstance(ctx context.Context, ec2c *ec2.Client, c cfg, ami, sgID string) (string, error) {
	out, err := ec2c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:Name"), Values: []string{c.name}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err == nil {
		for _, r := range out.Reservations {
			for _, inst := range r.Instances {
				id := aws.ToString(inst.InstanceId)
				log.Printf("reusing instance %s", id)
				return id, nil
			}
		}
	}

	_, subnetID, err := defaultVPCSubnet(ctx, ec2c)
	if err != nil {
		return "", err
	}
	res, err := ec2c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:            aws.String(ami),
		InstanceType:       ec2types.InstanceType(c.instanceType),
		KeyName:            aws.String(c.keyName),
		SecurityGroupIds:   []string{sgID},
		SubnetId:           aws.String(subnetID),
		MinCount:           aws.Int32(1),
		MaxCount:           aws.Int32(1),
		IamInstanceProfile: &ec2types.IamInstanceProfileSpecification{Name: aws.String(c.profileName)},
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeInstance,
			Tags:         []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String(c.name)}},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("run instances: %w", err)
	}
	id := aws.ToString(res.Instances[0].InstanceId)
	log.Printf("launched instance %s (%s)", id, c.instanceType)
	return id, nil
}

func waitInstanceReady(ctx context.Context, ec2c *ec2.Client, id string) error {
	if err := ec2.NewInstanceRunningWaiter(ec2c).Wait(ctx,
		&ec2.DescribeInstancesInput{InstanceIds: []string{id}}, 5*time.Minute); err != nil {
		return fmt.Errorf("wait running: %w", err)
	}
	if err := ec2.NewInstanceStatusOkWaiter(ec2c).Wait(ctx,
		&ec2.DescribeInstanceStatusInput{InstanceIds: []string{id}, IncludeAllInstances: aws.Bool(true)}, 5*time.Minute); err != nil {
		return fmt.Errorf("wait status ok: %w", err)
	}
	return nil
}

func publicIP(ctx context.Context, ec2c *ec2.Client, id string) (string, error) {
	out, err := ec2c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return "", err
	}
	if len(out.Reservations) == 0 || len(out.Reservations[0].Instances) == 0 {
		return "", fmt.Errorf("instance %s not found", id)
	}
	ip := out.Reservations[0].Instances[0].PublicIpAddress
	if ip == nil || *ip == "" {
		return "", fmt.Errorf("instance %s has no public IP", id)
	}
	return *ip, nil
}

// ---------------------------------------------------------------------------
// ssh
// ---------------------------------------------------------------------------

func dialSSH(ip, keyPath string) (*ssh.Client, error) {
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, err
	}
	cc := &ssh.ClientConfig{
		User:            "ec2-user",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // prototype: first-use trust
		Timeout:         10 * time.Second,
	}
	return ssh.Dial("tcp", net.JoinHostPort(ip, "22"), cc)
}

func waitSSH(ip, keyPath string) error {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		c, err := dialSSH(ip, keyPath)
		if err == nil {
			c.Close()
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("ssh to %s never became reachable", ip)
}

func runRemote(client *ssh.Client, script string) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var b bytes.Buffer
	sess.Stdout = &b
	sess.Stderr = &b
	if err := sess.Run(script); err != nil {
		return b.String(), err
	}
	return b.String(), nil
}

func uploadBinary(client *ssh.Client, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin = f
	return sess.Run("cat > /home/ec2-user/Bevcheck")
}

func installAndStart(ip, keyPath, binary, port, region string) error {
	client, err := dialSSH(ip, keyPath)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := uploadBinary(client, binary); err != nil {
		return fmt.Errorf("upload: %w", err)
	}

	unit := fmt.Sprintf(`[Unit]
Description=Bevcheck - TTB label verification monolith
After=network.target

[Service]
ExecStart=/usr/local/bin/Bevcheck
Environment=PORT=%s
Environment=AWS_REGION=%s
Restart=on-failure

[Install]
WantedBy=multi-user.target
`, port, region)

	script := "set -e\n" +
		"sudo systemctl stop Bevcheck 2>/dev/null || true\n" +
		"sudo install -m 0755 $HOME/Bevcheck /usr/local/bin/Bevcheck\n" +
		"sudo tee /etc/systemd/system/Bevcheck.service >/dev/null <<'EOF'\n" + unit + "EOF\n" +
		"sudo systemctl daemon-reload\n" +
		"sudo systemctl enable --now Bevcheck\n"

	if out, err := runRemote(client, script); err != nil {
		return fmt.Errorf("remote setup: %w\n%s", err, out)
	}
	log.Printf("service installed and started on %s", ip)
	return nil
}

func waitHealthy(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("health check %s did not pass", url)
}

// ---------------------------------------------------------------------------
// down
// ---------------------------------------------------------------------------

func down(c cfg) error {
	ctx := context.Background()
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(c.region))
	if err != nil {
		return fmt.Errorf("aws config: %w", err)
	}
	ec2c := ec2.NewFromConfig(awsCfg)
	iamc := iam.NewFromConfig(awsCfg)

	// terminate instance(s) by Name tag
	out, err := ec2c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:Name"), Values: []string{c.name}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	ids := []string{}
	if err == nil {
		for _, r := range out.Reservations {
			for _, inst := range r.Instances {
				ids = append(ids, aws.ToString(inst.InstanceId))
			}
		}
	}
	if len(ids) > 0 {
		if _, err := ec2c.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: ids}); err != nil {
			return fmt.Errorf("terminate: %w", err)
		}
		log.Printf("terminating %v", ids)
		if err := ec2.NewInstanceTerminatedWaiter(ec2c).Wait(ctx,
			&ec2.DescribeInstancesInput{InstanceIds: ids}, 5*time.Minute); err != nil {
			return fmt.Errorf("wait terminated: %w", err)
		}
		log.Printf("instance(s) terminated")
	} else {
		log.Printf("no instances with name %q", c.name)
	}

	// security group
	if id := sgIDByName(ctx, ec2c, c.sgName); id != "" {
		if _, err := ec2c.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: aws.String(id)}); err != nil {
			log.Printf("delete SG (ignored): %v", err)
		} else {
			log.Printf("deleted security group %s", c.sgName)
		}
	}

	// keypair (AWS side; local .pem removed too since it is now inert)
	if _, err := ec2c.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{KeyName: aws.String(c.keyName)}); err != nil {
		log.Printf("delete keypair (ignored): %v", err)
	} else {
		log.Printf("deleted keypair %s", c.keyName)
		if err := os.Remove(c.keyPath); err == nil {
			log.Printf("removed local key %s", c.keyPath)
		}
	}

	// instance profile + role (strip inline policies first, then the role)
	_, _ = iamc.RemoveRoleFromInstanceProfile(ctx, &iam.RemoveRoleFromInstanceProfileInput{
		InstanceProfileName: aws.String(c.profileName), RoleName: aws.String(c.roleName),
	})
	if _, err := iamc.DeleteInstanceProfile(ctx, &iam.DeleteInstanceProfileInput{
		InstanceProfileName: aws.String(c.profileName),
	}); err != nil {
		log.Printf("delete instance profile (ignored): %v", err)
	} else {
		log.Printf("deleted instance profile %s", c.profileName)
	}
	if pols, err := iamc.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: aws.String(c.roleName)}); err == nil {
		for _, name := range pols.PolicyNames {
			if _, err := iamc.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(c.roleName), PolicyName: aws.String(name)}); err != nil {
				log.Printf("delete role policy %s (ignored): %v", name, err)
			} else {
				log.Printf("deleted role policy %s", name)
			}
		}
	}
	if _, err := iamc.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(c.roleName)}); err != nil {
		log.Printf("delete role (ignored): %v", err)
	} else {
		log.Printf("deleted role %s", c.roleName)
	}

	fmt.Println("teardown complete")
	return nil
}
