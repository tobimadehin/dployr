package installer

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tobimadehin/dployr/utils"
)

const (
	CDN     = "https://github.com/tobimadehin/dployr/releases/download"
)

type Installer struct {
	installType     string
	randomSubdomain string
	stateDir        string
	publicIP        string
    privateIP       string   
	startTime       time.Time
	u               *utils.Utils
}

type Release struct {
    TagName string `json:"tag_name"`
}

type DNSResponse struct {
	Success bool `json:"success"`
	Errors  struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func NewInstaller() *Installer {
	usr, _ := user.Current()
	stateDir := filepath.Join(usr.HomeDir, ".dployr", "state")
	os.MkdirAll(stateDir, 0775)

	timestamp := time.Now().Format("20060102-150405")
	logFile := fmt.Sprintf("/tmp/dployr-%s.log", timestamp)

	u := utils.NewUtils(50, logFile)
	return &Installer{
		stateDir:        stateDir,
		randomSubdomain: u.GenerateRandomHex(6),
		startTime:       time.Now(),
		u:               u,
	}
}

func (i *Installer) Run(installType string) error {
	if installType != "docker" && installType != "standalone" {
		return fmt.Errorf("invalid install type. Use 'docker' or 'standalone'")
	}

	i.installType = installType
	i.u.LogInfo("Starting dployr installer...")

	if err := i.checkSudo(); err != nil {
		return err
	}

	if err := i.getServerIP(); err != nil {
		return err
	}

	fmt.Printf("\nInstalling dployr (%s mode)...\n", installType)
	fmt.Println() // Extra line for progress bar space

	steps := []struct {
		name string
		fn   func() error
	}{
		{"Creating user", i.createDployrUser},
		{"Installing requirements", i.installRequirements},
		{"Downloading archive", i.downloadDployr},
		{"Setting up docker", i.setupDocker},
		{"Creating directories", i.setupDirectories},
		{"Running migrations", i.runMigrations},
		{"Configuring services", i.configureServices},
		{"Setting up caddy", i.setupCaddy},
		{"Starting services", i.startDployr},
	}

	for idx, step := range steps {
		i.u.ShowProgress(idx, len(steps), step.name+"...")
		if err := step.fn(); err != nil {
			fmt.Print("\n\n") // New lines before error
			i.u.HandleError(step.name, err)
			return err
		}
		i.u.ShowProgress(idx+1, len(steps), step.name+" ✓")
	}

	i.u.ShowProgress(len(steps), len(steps), "Complete!")
	i.showCompletion()
	return nil
}

func (i *Installer) checkSudo() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("this installer must be run as root or with sudo privileges")
	}
	return nil
}

func (i *Installer) getServerIP() error {
	resp, err := http.Get("https://api.ipify.org")
	if err != nil {
		return fmt.Errorf("failed to get server IP: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read IP response: %w", err)
	}

	i.publicIP = strings.TrimSpace(string(body))
	// Get private IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("failed to get interfaces: %w", err)
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue // skip down or loopback
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if ip = ip.To4(); ip != nil {
				i.privateIP = ip.String()
				return nil
			}
		}
	}

	return fmt.Errorf("no private IP found")
}

func (i *Installer) createDployrUser() error {
	flagFile := filepath.Join(i.stateDir, "create_dployr_user.flag")
	if i.u.FileExists(flagFile) {
		i.u.UpdateStatus("User already exists")
		return nil
	}

	// Check if user exists
	if _, err := user.Lookup("dployr"); err != nil {
		i.u.UpdateStatus("Creating dployr user...")
		if err := i.u.RunCommand("useradd", "-r", "-m", "-s", "/bin/bash", "-d", "/home/dployr", "dployr"); err != nil {
			return fmt.Errorf("failed to create dployr user: %w", err)
		}
	} else {
		i.u.UpdateStatus("User already exists")
	}

	// Add to docker group if Docker installation
	if i.installType == "docker" {
		i.u.UpdateStatus("Adding user to docker group...")
		cmd := exec.Command("usermod", "-aG", "docker", "dployr")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add dployr to docker group: %w", err)
		}
	}

	// Create sudo rules
	i.u.UpdateStatus("Setting up sudo permissions...")
	sudoContent := `# Allow dployr to manage its own service and nginx
	dployr ALL=(ALL) NOPASSWD: /bin/systemctl start dployr, /bin/systemctl stop dployr, /bin/systemctl restart dployr, /bin/systemctl reload nginx, /bin/systemctl restart nginx
	`
	if err := i.u.WriteFile("/etc/sudoers.d/dployr", sudoContent, 0440); err != nil {
		return fmt.Errorf("failed to create sudo rules: %w", err)
	}

	return i.u.TouchFile(flagFile)
}

func (i *Installer) downloadDployr() error {
	flagFile := filepath.Join(i.stateDir, "download_dployr.flag")
	if i.u.FileExists(flagFile) {
		i.u.UpdateStatus("Binary already downloaded")
		return nil
	}

	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		// keep as is
	case "arm":
		// keep as is
	default:
		return fmt.Errorf("unsupported architecture: %s", arch)
	}

	i.u.UpdateStatus("Creating dployr directory...")
	serverDir := "/home/dployr"
	if err := os.MkdirAll(serverDir, 0775); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	i.u.UpdateStatus("Fetching latest release info...")
	resp, err := http.Get("https://api.github.com/repos/tobimadehin/dployr/releases/latest")
	if err != nil {
		return fmt.Errorf("failed to fetch release info: %w", err)
	}
	defer resp.Body.Close()

	i.u.UpdateStatus("Parsing release data...")
	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return fmt.Errorf("failed to parse release data: %w", err)
	}

	downloadURL := fmt.Sprintf("%s/%s/dployr-%s.zip", CDN, release.TagName, release.TagName)
	i.u.UpdateStatus(fmt.Sprintf("Starting download from %s...", downloadURL))

	tmpFile := filepath.Join(serverDir, "dployr.zip")
	i.u.UpdateStatus("Downloading ZIP file...")
	if err := i.u.RunCommand("curl", "-fsSL", "-o", tmpFile, downloadURL); err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	i.u.UpdateStatus("Extracting...")
	if err := i.u.RunCommand("unzip", "-q", tmpFile, "-d", serverDir); err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}

	os.Remove(tmpFile)

	i.u.UpdateStatus("Setting path permissions...")
	if err := os.Chmod(serverDir, 0775); err != nil {
		return fmt.Errorf("failed to set path permissions: %w", err)
	}

	i.u.UpdateStatus("Setting file ownership...")
	if err := i.u.RunCommand("chown", "-R", "dployr:dployr", "/home/dployr"); err != nil {
		return fmt.Errorf("failed to change ownership: %w", err)
	}
	// TODO: Refactor to use os api
	if err := i.u.RunCommand("chown", "-R", "dployr:www-data", "/home/dployr/storage", "/home/dployr/bootstrap/cache"); err != nil {
		return fmt.Errorf("failed to change ownership: %w", err)
	}
	if err := os.Chmod("/home/dployr/bootstrap/cache", 0775); err != nil {
		return fmt.Errorf("failed to set path permissions: %w", err)
	}
	if err := os.Chmod("/home/dployr/storage", 0775); err != nil {
		return fmt.Errorf("failed to set path permissions: %w", err)
	}

	i.u.UpdateStatus("Download complete")
	return i.u.TouchFile(flagFile)
}

func (i *Installer) installRequirements() error {
	flagFile := filepath.Join(i.stateDir, "install_requirements.flag")
	if i.u.FileExists(flagFile) {
		i.u.UpdateStatus("Requirements already installed")
		return nil
	}

	osType := i.u.GetOSType()
	i.u.UpdateStatus(fmt.Sprintf("Detected OS: %s", osType))

	var err error
	switch osType {
	case "ubuntu":
		err = i.installUbuntuPackages()
	case "debian":
		err = i.installDebianPackages()
	case "centos", "rhel", "rocky", "alma":
		err = i.installRHELPackages()
	default:
		return fmt.Errorf("unsupported OS: %s", osType)
	}

	if err != nil {
		return err
	}

	return i.u.TouchFile(flagFile)
}

func (i *Installer) installUbuntuPackages() error {
	// Update packages
	i.u.UpdateStatus("Updating package lists...")
	if err := i.u.RunCommand("apt-get", "update", "-qq"); err != nil {
		return fmt.Errorf("failed to update packages: %w", err)
	}

	i.u.UpdateStatus("Installing system packages...")
	basicPackages := []string{"curl", "wget", "git", "jq", "ca-certificates", "gnupg", "ufw", "openssl", "unzip", "net-tools" }
	if err := i.u.RunCommand("apt-get", append([]string{"install", "-y"}, basicPackages...)...); err != nil {
		return fmt.Errorf("failed to install basic packages: %w", err)
	}

	i.u.UpdateStatus("Update repository with php packages...")
	if err := i.u.RunCommand("add-apt-repository", "ppa:ondrej/php"); err != nil {
		return fmt.Errorf("failed to add ppa:ondrej/php repository: %s", err)
	}

	i.u.UpdateStatus("Updating package lists...")
	if err := i.u.RunCommand("apt-get", "update", "-qq"); err != nil {
		return fmt.Errorf("failed to update packages: %w", err)
	}

	i.u.UpdateStatus("Install php packages...")
	phpPackages := []string{"composer", "php8.3-common", "php8.3-cli", "php8.3-fpm", "php8.3-curl","php8.3-bz2", "php8.3-mbstring", "php8.3-intl"}
	if err := i.u.RunCommand("apt-get", append([]string{"install", "-y"}, phpPackages...)...); err != nil {
		return fmt.Errorf("failed to install php packages: %s", err)
	}

	// Install Caddy
	i.u.UpdateStatus("Installing Caddy web server...")
	if err := i.installCaddyDebian(); err != nil {
		return fmt.Errorf("failed to install Caddy: %w", err)
	}

	// Install Docker if needed
	if i.installType == "docker" {
		i.u.UpdateStatus("Installing Docker...")
		if err := i.installDockerDebian(); err != nil {
			return fmt.Errorf("failed to install Docker: %w", err)
		}
	}

	return nil
}

func (i *Installer) installDebianPackages() error {
	// Update packages
	i.u.UpdateStatus("Updating package lists...")
	if err := i.u.RunCommand("apt-get", "update", "-qq"); err != nil {
		return fmt.Errorf("failed to update packages: %w", err)
	}

	// Install basic packages
	i.u.UpdateStatus("Installing system packages...")
	basicPackages := []string{"curl", "wget", "git", "jq", "ca-certificates", "gnupg", "ufw", "openssl", "unzip",
		"net-tools", "debian-keyring", "debian-archive-keyring", "apt-transport-https", "lsb-release"}
	if err := i.u.RunCommand("apt-get", append([]string{"install", "-y"}, basicPackages...)...); err != nil {
		return fmt.Errorf("failed to install basic packages: %w", err)
	}

	// Create keyring directory
	i.u.UpdateStatus("Creating keyring directory...")
	if err := i.u.RunCommand("mkdir", "-p", "/usr/share/keyrings"); err != nil {
		return fmt.Errorf("failed to create keyring directory: %w", err)
	}

	// Add before "Adding PHP repository GPG key..." 
	i.u.UpdateStatus("Testing DNS resolution...")
	if err := i.u.RunCommand("nslookup", "packages.sury.org"); err != nil {
		i.u.LogWarning("DNS resolution failed for packages.sury.org")
	}

	// Add GPG key 
	i.u.UpdateStatus("Adding PHP repository GPG key...")
	keyCmd := "curl -v -fsSL https://packages.sury.org/php/apt.gpg | gpg --dearmor -o /usr/share/keyrings/sury-php-keyring.gpg"
	if err := i.u.RunCommand("sh", "-c", keyCmd); err != nil {
		return fmt.Errorf("failed to add sury GPG key: %w", err)
	}

	// Set permissions
	if err := i.u.RunCommand("chmod", "664", "/usr/share/keyrings/sury-php-keyring.gpg"); err != nil {
		return fmt.Errorf("failed to set keyring permissions: %w", err)
	}

	// Add repository 
	i.u.UpdateStatus("Adding PHP repository...")
	codename, err := i.u.GetLSBCodename()
	if err != nil {
		return fmt.Errorf("failed to get codename: %w", err)
	}
	repoContent := fmt.Sprintf("deb [signed-by=/usr/share/keyrings/sury-php-keyring.gpg] https://packages.sury.org/php/ %s main\n", codename)
	if err := i.u.WriteFile("/etc/apt/sources.list.d/sury-php.list", repoContent, 0664); err != nil {
		return fmt.Errorf("failed to add sury repository: %w", err)
	}

	// Update package lists 
	i.u.UpdateStatus("Updating package lists...")
	if err := i.u.RunCommand("apt-get", "update", "-qq"); err != nil {
		return fmt.Errorf("failed to update packages: %w", err)
	}

	// Install PHP packages
	i.u.UpdateStatus("Installing PHP packages...")
	phpPackages := []string{"php8.3-fpm", "php8.3-cli", "php8.3-common", "php8.3-curl", "php8.3-mbstring", 
		"php8.3-xml", "php8.3-zip", "php8.3-bcmath", "php8.3-intl", "php8.3-gd", "php8.3-sqlite3", 
		"php8.3-tokenizer", "composer"}
	if err := i.u.RunCommand("apt-get", append([]string{"install", "-y"}, phpPackages...)...); err != nil {
		return fmt.Errorf("failed to install PHP packages: %w", err)
	}

	// Install Caddy
	i.u.UpdateStatus("Installing Caddy web server...")
	if err := i.installCaddyDebian(); err != nil {
		return fmt.Errorf("failed to install Caddy: %w", err)
	}

	// Install Docker if needed
	if i.installType == "docker" {
		i.u.UpdateStatus("Installing Docker...")
		if err := i.installDockerDebian(); err != nil {
			return fmt.Errorf("failed to install Docker: %w", err)
		}
	}

	return nil
}

func (i *Installer) installRHELPackages() error {
	// Install Caddy
	if err := i.u.RunCommand("yum", "install", "-y", "yum-plugin-copr"); err != nil {
		return err
	}
	if err := i.u.RunCommand("yum", "copr", "enable", "-y", "@caddy/caddy"); err != nil {
		return err
	}

	basicPackages := []string{"curl", "wget", "git", "jq", "caddy", "ufw", "openssl"}
	if err := i.u.RunCommand("yum", append([]string{"install", "-y"}, basicPackages...)...); err != nil {
		return err
	}

	// Install Docker if needed
	if i.installType == "docker" {
		if err := i.u.RunCommand("yum", "install", "-y", "yum-utils"); err != nil {
			return err
		}
		if err := i.u.RunCommand("yum-config-manager", "--add-repo", "https://download.docker.com/linux/centos/docker-ce.repo"); err != nil {
			return err
		}
		dockerPackages := []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"}
		if err := i.u.RunCommand("yum", append([]string{"install", "-y"}, dockerPackages...)...); err != nil {
			return err
		}
	}

	return nil
}

func (i *Installer) installCaddyDebian() error {
	// Add GPG key
	cmd1 := exec.Command("curl", "-1sLf", "https://dl.cloudsmith.io/public/caddy/stable/gpg.key")
	cmd2 := exec.Command("gpg", "--dearmor", "-o", "/usr/share/keyrings/caddy-stable-archive-keyring.gpg")

	pipe, err := cmd1.StdoutPipe()
	if err != nil {
		return err
	}
	cmd2.Stdin = pipe

	if err := cmd1.Start(); err != nil {
		return err
	}
	if err := cmd2.Start(); err != nil {
		return err
	}
	if err := cmd1.Wait(); err != nil {
		return err
	}
	if err := cmd2.Wait(); err != nil {
		return err
	}

	// Add repository using the official method
	cmd3 := exec.Command("curl", "-1sLf", "https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt")
	cmd4 := exec.Command("tee", "/etc/apt/sources.list.d/caddy-stable.list")

	pipe2, err := cmd3.StdoutPipe()
	if err != nil {
		return err
	}
	cmd4.Stdin = pipe2

	if err := cmd3.Start(); err != nil {
		return err
	}
	if err := cmd4.Start(); err != nil {
		return err
	}
	if err := cmd3.Wait(); err != nil {
		return err
	}
	if err := cmd4.Wait(); err != nil {
		return err
	}

	// Set permissions
	if err := os.Chmod("/usr/share/keyrings/caddy-stable-archive-keyring.gpg", 0664); err != nil {
		return err
	}
	if err := os.Chmod("/etc/apt/sources.list.d/caddy-stable.list", 0664); err != nil {
		return err
	}

	// Update and install
	if err := i.u.RunCommand("apt-get", "update", "-qq"); err != nil {
		return err
	}
	return i.u.RunCommand("apt-get", "install", "-y", "caddy")
}

func (i *Installer) installDockerDebian() error {
	osType := i.u.GetOSType()

	// Add Docker's GPG key
	cmd1 := exec.Command("curl", "-fsSL", fmt.Sprintf("https://download.docker.com/linux/%s/gpg", osType))
	cmd2 := exec.Command("gpg", "--dearmor", "-o", "/etc/apt/keyrings/docker.gpg")

	os.MkdirAll("/etc/apt/keyrings", 0775)

	pipe, err := cmd1.StdoutPipe()
	if err != nil {
		return err
	}
	cmd2.Stdin = pipe

	if err := cmd1.Start(); err != nil {
		return err
	}
	if err := cmd2.Start(); err != nil {
		return err
	}
	if err := cmd1.Wait(); err != nil {
		return err
	}
	if err := cmd2.Wait(); err != nil {
		return err
	}

	if err := os.Chmod("/etc/apt/keyrings/docker.gpg", 0664); err != nil {
		return err
	}

	// Add Docker repository
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "amd64"
	}

	codename, err := i.u.GetLSBCodename()
	if err != nil {
		return err
	}

	repoContent := fmt.Sprintf("deb [arch=%s signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/%s %s stable\n", arch, osType, codename)
	if err := i.u.WriteFile("/etc/apt/sources.list.d/docker.list", repoContent, 0664); err != nil {
		return err
	}

	// Update and install Docker
	if err := i.u.RunCommand("apt-get", "update", "-qq"); err != nil {
		return err
	}

	dockerPackages := []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"}
	return i.u.RunCommand("apt-get", append([]string{"install", "-y"}, dockerPackages...)...)
}

func (i *Installer) setupDocker() error {
	if i.installType != "docker" {
		return nil
	}

	flagFile := filepath.Join(i.stateDir, "setup_docker.flag")
	if i.u.FileExists(flagFile) {
		return nil
	}

	// Create Docker daemon config
	if err := os.MkdirAll("/etc/docker", 0775); err != nil {
		return fmt.Errorf("failed to create /etc/docker: %w", err)
	}

	daemonConfig := `{
    "log-driver": "json-file",
    "log-opts": {
        "max-size": "100m",
        "max-file": "10"
    },
    "default-address-pools": [
        {"base": "172.17.0.0/12", "size": 20}
    ]
}`

	if err := i.u.WriteFile("/etc/docker/daemon.json", daemonConfig, 0664); err != nil {
		return fmt.Errorf("failed to write Docker daemon config: %w", err)
	}

	// Enable and start Docker
	if err := i.u.RunCommand("systemctl", "enable", "docker"); err != nil {
		return fmt.Errorf("failed to enable Docker: %w", err)
	}
	if err := i.u.RunCommand("systemctl", "start", "docker"); err != nil {
		return fmt.Errorf("failed to start Docker: %w", err)
	}

	// Wait for Docker to be ready
	time.Sleep(5 * time.Second)

	// Open firewall
	if err := i.u.RunCommand("ufw", "allow", "7879"); err != nil {
		i.u.LogWarning("Failed to open firewall port 7879")
	}

	return i.u.TouchFile(flagFile)
}

func (i *Installer) setupDirectories() error {
	var dirs []string
	if i.installType == "docker" {
		dirs = []string{
			"/data/dployr/nextjs-apps",
			"/data/dployr/builds",
			"/data/dployr/images/cache",
			"/data/dployr/logs/hot",
			"/data/dployr/logs/warm",
			"/data/dployr/logs/cold",
			"/data/dployr/monitoring/prometheus",
			"/data/dployr/monitoring/grafana",
			"/data/dployr/ssl",
			"/data/dployr/nginx/sites",
			"/data/dployr/redis",
		}
	} else {
		dirs = []string{
			"/home/dployr/apps",
			"/home/dployr/builds",
			"/home/dployr/logs",
			"/home/dployr/ssl",
		}
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0775); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Set ownership
	var chownPath string
	if i.installType == "docker" {
		chownPath = "/data/dployr"
	} else {
		chownPath = "/home/dployr"
		// Create log file
		if err := i.u.TouchFile("/var/log/dployr.log"); err != nil {
			return fmt.Errorf("failed to create log file: %w", err)
		}
		if err := i.u.RunCommand("chown", "dployr:dployr", "/var/log/dployr.log"); err != nil {
			return fmt.Errorf("failed to set log file ownership: %w", err)
		}
	}

	if err := i.u.RunCommand("chown", "-R", "dployr:dployr", chownPath); err != nil {
		return fmt.Errorf("failed to set ownership: %w", err)
	}

	return nil
}

func (i *Installer) runMigrations() error {
	appPath := "/home/dployr"

	i.u.UpdateStatus("Updating app")
	if err := i.u.RunCommand("sudo", "-u", "dployr", "composer", "-d", appPath, "run", "post-create-project-cmd"); err != nil {
		return fmt.Errorf("sqlite3 migrations failed: %w", err)
	}

	if err := i.u.RunCommand("chown", "dployr:www-data", "/home/dployr/database/database.sqlite"); err != nil {
		return fmt.Errorf("failed to set log file ownership: %w", err)
	}
	if err := i.u.RunCommand("chown", "dployr:www-data", "/home/dployr/database"); err != nil {
		return fmt.Errorf("failed to set log directory ownership: %w", err)
	}
	if err := os.Chmod("/home/dployr/database/database.sqlite", 0664); err != nil {
		return err
	}
	if err := os.Chmod("/home/dployr/database", 0775); err != nil {
		return err
	}

	i.u.UpdateStatus("app setup completed")
	return nil
}

func (i *Installer) configureServices() error {
	if i.installType == "docker" {
		return i.setupDockerCompose()
	}
	return i.createSystemdService()
}

func (i *Installer) setupDockerCompose() error {
	composeContent := `services:
  dployr-web:
    image: dployr:latest
    user: "dployr:dployr" 
    ports: 
      - "7879:7879"
    volumes:
      - /data/dployr:/data
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      - NODE_ENV=production
      - NEXT_TELEMETRY_DISABLED=1
    restart: unless-stopped
`

	if err := i.u.WriteFile("/data/dployr/docker-compose.yml", composeContent, 0664); err != nil {
		return fmt.Errorf("failed to create docker-compose.yml: %w", err)
	}

	return nil
}

func (i *Installer) createSystemdService() error {
	appFolder := "/home/dployr"

	serviceContent := fmt.Sprintf(`[Unit]
Description=dployr 
After=network.target

[Service]
User=dployr
Group=dployr
WorkingDirectory=%s
ExecStart=/usr/bin/php %s/artisan queue:work --sleep=3 --tries=3
Restart=always
StandardOutput=append:/var/log/dployr.log
StandardError=append:/var/log/dployr.log

[Install]
WantedBy=multi-user.target
`, appFolder, appFolder)

	if err := i.u.WriteFile("/etc/systemd/system/dployr.service", serviceContent, 0664); err != nil {
		return fmt.Errorf("failed to create systemd service: %w", err)
	}
	if err := i.u.RunCommand("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}
	if err := i.u.RunCommand("sh", "-c", "chmod +x /home/dployr/artisan"); err != nil {
		return fmt.Errorf("failed to set artisan as an executable: %w", err)
	} 
	if err := i.u.RunCommand("systemctl", "enable", "dployr"); err != nil {
		return fmt.Errorf("failed to enable dployr service: %w", err)
	}

	return nil
}

func (i *Installer) setupCaddy() error {
	if err := i.createCloudflareRecord(); err != nil {
		return fmt.Errorf("failed to create DNS record: %w", err)
	}

	domain := fmt.Sprintf("%s.dployr.dev", i.randomSubdomain)
    appFolder := "/home/dployr"
    
    var caddyContent string
    if i.publicIP != i.privateIP {
        // Behind NAT - serve on both domain and private IP
        caddyContent = fmt.Sprintf(`{
    auto_https disable_redirects
}
			
%s {
    root * %s/public
    php_fastcgi unix//run/php/php8.3-fpm.sock
    try_files {path} {path}/ /index.php?{query}
    file_server
}

http://%s:80 {
    root * %s/public
    php_fastcgi unix//run/php/php8.3-fpm.sock
    try_files {path} {path}/ /index.php?{query}
    file_server
}`, domain, appFolder, i.privateIP, appFolder)
    } else {
        // Direct internet access
        caddyContent = fmt.Sprintf(`%s {
    root * %s/public
    php_fastcgi unix//run/php/php8.3-fpm.sock
    try_files {path} {path}/ /index.php?{query}
    file_server
}`, domain, appFolder)
    }
	
	if err := i.u.WriteFile("/etc/caddy/Caddyfile", caddyContent, 0664); err != nil {
		return fmt.Errorf("failed to create Caddyfile: %w", err)
	}

	// Set permissions
	if err := i.u.RunCommand("chown", "caddy:caddy", "/etc/caddy/Caddyfile"); err != nil {
		return fmt.Errorf("failed to set Caddyfile ownership: %w", err)
	}

	// Open firewall ports
	i.u.RunCommand("ufw", "allow", "80")
	i.u.RunCommand("ufw", "allow", "443")

	// Wait for DNS propagation
	i.u.UpdateStatus("Waiting for DNS propagation (30s)...")
	time.Sleep(30 * time.Second)

	// Reload Caddy
	i.u.UpdateStatus("Reloading Caddy configuration...")
	if err := i.u.RunCommand("systemctl", "restart", "caddy"); err != nil {
		return fmt.Errorf("failed to restart Caddy: %w", err)
	}

	// Wait for SSL certificate provisioning
	i.u.UpdateStatus("Provisioning SSL certificate...")
	time.Sleep(15 * time.Second)

	return nil
}

func (i *Installer) createCloudflareRecord() error {
	i.u.UpdateStatus(fmt.Sprintf("Creating DNS record for %s.dployr.dev...", i.randomSubdomain))

	payload := map[string]string{
		"subdomain": i.randomSubdomain,
		"host":      i.privateIP,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	resp, err := http.Post("https://dployr.dev/api/dns/create", "application/json", strings.NewReader(string(jsonData)))
	if err != nil {
		return fmt.Errorf("failed to create DNS record: %w", err)
	}
	defer resp.Body.Close()

	var dnsResp DNSResponse
	if err := json.NewDecoder(resp.Body).Decode(&dnsResp); err != nil {
		return fmt.Errorf("failed to decode DNS response: %w", err)
	}

	if !dnsResp.Success {
		return fmt.Errorf("DNS record creation failed: %s", dnsResp.Errors.Message)
	}

	return nil
}

func (i *Installer) startDployr() error {
	flagFile := filepath.Join(i.stateDir, "start_dployr.flag")
	if i.u.FileExists(flagFile) {
		return nil
	}

	if i.installType == "docker" {
		if err := i.u.RunCommandInDir("/data/dployr", "docker", "compose", "up", "-d"); err != nil {
			return fmt.Errorf("failed to start dployr (Docker): %w", err)
		}
		time.Sleep(5 * time.Second)
	} else {
		if err := i.u.RunCommand("systemctl", "start", "dployr"); err != nil {
			return fmt.Errorf("failed to start dployr (Systemd): %w", err)
		}
		time.Sleep(3 * time.Second)
	}

	return i.u.TouchFile(flagFile)
}

func (i *Installer) showCompletion() {
	duration := time.Since(i.startTime)
	minutes := int(duration.Minutes())
	seconds := int(duration.Seconds()) % 60

	fmt.Println("\n\n╔══════════════════════════════════════╗")
	fmt.Println("║         INSTALLATION COMPLETE        ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Printf("\n%s[SUCCESS]%s Installation completed in %dm %ds\n", utils.ColorGreen, utils.ColorNC, minutes, seconds)

	fmt.Printf("\n%sAccess your dployr installation at:%s\n", utils.ColorBlue, utils.ColorNC)
	fmt.Printf("  %shttps://%s.dployr.dev%s\n\n", utils.ColorGreen, i.randomSubdomain, utils.ColorNC)

	fmt.Println("Service management:")
	if i.installType == "docker" {
		fmt.Println("  Start:   cd /data/dployr && docker compose up -d")
		fmt.Println("  Stop:    cd /data/dployr && docker compose down")
		fmt.Println("  Logs:    cd /data/dployr && docker compose logs -f")
	} else {
		fmt.Println("  Start:   sudo systemctl start dployr")
		fmt.Println("  Stop:    sudo systemctl stop dployr")
		fmt.Println("  Status:  sudo systemctl status dployr")
		fmt.Println("  Logs:    tail -f /var/log/dployr.log")
	}
	fmt.Println()
}
