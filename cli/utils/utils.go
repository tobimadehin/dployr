package utils

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	ColorRed    = "\033[0;31m"
	ColorGreen  = "\033[0;32m"
	ColorYellow = "\033[1;33m"
	ColorBlue   = "\033[0;34m"
	ColorNC     = "\033[0m"
)

type Utils struct {
	progressWidth int
	logFile       string
	currentStatus string
}

// NewUtils creates a configured Utils helper
func NewUtils(progressWidth int, logFile string) *Utils {
	return &Utils{progressWidth: progressWidth, logFile: logFile, currentStatus: ""}
}

func (u *Utils) UpdateStatus(status string) {
	u.currentStatus = status
	// Log all status updates for full history
	u.writeLog(fmt.Sprintf("[STATUS] %s", status))
}

func (u *Utils) ShowProgress(current, total int, message string) {
	percentage := (current * 100) / total
	completed := (current * u.progressWidth) / total

	// Clear current line and move cursor up to clear status line too
	fmt.Print("\r\033[K\033[A\r\033[K")

	// Build progress bar
	bar := "["
	for j := 0; j < completed; j++ {
		bar += "█"
	}
	for j := completed; j < u.progressWidth; j++ {
		bar += "░"
	}
	bar += "]"

	// Print progress with color
	fmt.Printf("%s%s%s %s%3d%%%s %s\n", ColorBlue, bar, ColorNC, ColorGreen, percentage, ColorNC, message)

	// Print current status on the line below
	if u.currentStatus != "" {
		fmt.Printf("%s└─%s %s", ColorYellow, ColorNC, u.currentStatus)
	} else {
		fmt.Print(" ")
	}

	if current == total {
		fmt.Print("\n")
	}

	// Log progress updates for full history
	u.writeLog(fmt.Sprintf("[PROGRESS] %d/%d %s", current, total, message))
}

func (u *Utils) LogInfo(msg string) {
	u.UpdateStatus(msg)
	u.writeLog(fmt.Sprintf("[INFO] %s", msg))
}

func (u *Utils) LogSuccess(msg string) {
	u.UpdateStatus(msg)
	u.writeLog(fmt.Sprintf("[SUCCESS] %s", msg))
}

func (u *Utils) LogWarning(msg string) {
	u.UpdateStatus(msg)
	u.writeLog(fmt.Sprintf("[WARNING] %s", msg))
}

func (u *Utils) LogError(msg string) {
	fmt.Printf("%s[ERROR]%s %s\n", ColorRed, ColorNC, msg)
	u.writeLog(fmt.Sprintf("[ERROR] %s", msg))
}

func (u *Utils) writeLog(msg string) {
	f, err := os.OpenFile(u.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(fmt.Sprintf("%s %s\n", time.Now().Format("2006-01-02 15:04:05"), msg))
}

func (u *Utils) HandleError(step string, err error) {
	errorMsg := fmt.Sprintf("Installation failed during: %s", step)
	
	// Log the full error details first
	u.writeLog(fmt.Sprintf("[ERROR] %s", errorMsg))
	if err != nil {
		u.writeLog(fmt.Sprintf("[ERROR] Error details: %s", err.Error()))
	}
	
	// Then display to user
	fmt.Printf("\n\n%s[ERROR]%s %s\n", ColorRed, ColorNC, errorMsg)
	if err != nil {
		fmt.Printf("Error: %s\n", err.Error())
	}
	fmt.Printf("\nFull log available at: %s\n\n", u.logFile)
}

func (u *Utils) FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (u *Utils) TouchFile(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func (u *Utils) WriteFile(path, content string, perm os.FileMode) error {
	return os.WriteFile(path, []byte(content), perm)
}

func (u *Utils) RunCommand(name string, args ...string) error {
	// Update status to show what command is running
	cmdStr := fmt.Sprintf("Running: %s %s", name, strings.Join(args, " "))
	u.UpdateStatus(cmdStr)
	u.writeLog(fmt.Sprintf("[CMD] %s", cmdStr))

	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "DEBCONF_NONINTERACTIVE_SEEN=true")

	// Capture output for logging
	output, err := cmd.CombinedOutput()
	
	// Log the raw output first
	if len(output) > 0 {
		u.writeLog(fmt.Sprintf("[CMD_OUTPUT] %s", strings.TrimSpace(string(output))))
	}
	
	if err != nil {
		// Log detailed error information
		u.writeLog(fmt.Sprintf("[CMD_ERROR] Command failed with exit code: %v", err))
		u.writeLog(fmt.Sprintf("[CMD_ERROR] Command: %s %s", name, strings.Join(args, " ")))
		
		if len(output) > 0 {
			u.writeLog(fmt.Sprintf("[CMD_ERROR] Full error output: %s", strings.TrimSpace(string(output))))
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(output)))
		}
		return err
	}
	
	u.writeLog("[CMD_SUCCESS] Command completed successfully")
	return nil
}

func (u *Utils) RunCommandInDir(dir, name string, args ...string) error {
	// Update status to show what command is running
	cmdStr := fmt.Sprintf("Running in %s: %s %s", dir, name, strings.Join(args, " "))
	u.UpdateStatus(cmdStr)
	u.writeLog(fmt.Sprintf("[CMD] %s", cmdStr))

	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "DEBCONF_NONINTERACTIVE_SEEN=true")

	// Capture output for logging
	output, err := cmd.CombinedOutput()
	
	// Log the raw output first
	if len(output) > 0 {
		u.writeLog(fmt.Sprintf("[CMD_OUTPUT] %s", strings.TrimSpace(string(output))))
	}
	
	if err != nil {
		// Log detailed error information
		u.writeLog(fmt.Sprintf("[CMD_ERROR] Command failed with exit code: %v", err))
		u.writeLog(fmt.Sprintf("[CMD_ERROR] Command: %s %s (in %s)", name, strings.Join(args, " "), dir))
		
		if len(output) > 0 {
			u.writeLog(fmt.Sprintf("[CMD_ERROR] Full error output: %s", strings.TrimSpace(string(output))))
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(output)))
		}
		return err
	}
	
	u.writeLog("[CMD_SUCCESS] Command completed successfully")
	return nil
}

func (u *Utils) GetOSType() string {
	content, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "unknown"
	}

	re := regexp.MustCompile(`^ID="?([^"]+)"?`)
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		if matches := re.FindStringSubmatch(line); matches != nil {
			return matches[1]
		}
	}
	return "unknown"
}

func (u *Utils) GenerateRandomHex(length int) string {
	cmd := exec.Command("openssl", "rand", "-hex", strconv.Itoa(length))
	output, err := cmd.Output()
	if err != nil {
		// Fallback to simple random generation
		return fmt.Sprintf("%x", time.Now().UnixNano())[:length*2]
	}
	return strings.TrimSpace(string(output))
}

// GetLSBCodename returns the Debian/Ubuntu codename, e.g., "jammy"
func (u *Utils) GetLSBCodename() (string, error) {
	content, err := os.ReadFile("/etc/os-release")
	if err == nil {
		re := regexp.MustCompile(`(?m)^VERSION_CODENAME=\"?([^\"\n]+)\"?`)
		if matches := re.FindStringSubmatch(string(content)); matches != nil {
			return matches[1], nil
		}
	}

	if err := exec.Command("which", "lsb_release").Run(); err == nil {
		out, err := exec.Command("lsb_release", "-cs").Output()
		if err == nil {
			return strings.TrimSpace(string(out)), nil
		}
	}
	return "", fmt.Errorf("could not determine VERSION_CODENAME")
}
