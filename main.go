package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chzyer/readline"
)

// ANSI color codes
const (
	Reset   = "\033[0m"
	Bold    = "\033[1m"
	Cyan    = "\033[36m"
	Yellow  = "\033[33m"
	Green   = "\033[32m"
	Red     = "\033[31m"
	Magenta = "\033[35m"
	Dim     = "\033[2m"
)

// Config represents the user's configuration
type Config struct {
	Workspace      string          `json:"workspace"`
	RepoSlug       string          `json:"repo_slug"`
	Username       string          `json:"username"`
	Team           []TeamMember    `json:"team"`
	PreviewServers []PreviewServer `json:"preview_servers,omitempty"`
}

type PreviewServer struct {
	Name    string `json:"name"`
	Command string `json:"command"` // Command template, use {branch} as placeholder
}

type Author struct {
	DisplayName string `json:"display_name"`
	Nickname    string `json:"nickname"`
}

type Branch struct {
	Name string `json:"name"`
}

type Source struct {
	Branch Branch `json:"branch"`
}

type User struct {
	DisplayName string `json:"display_name"`
	Nickname    string `json:"nickname"`
}

type Participant struct {
	Approved bool   `json:"approved"`
	User     User   `json:"user"`
	Role     string `json:"role"`
}

type PullRequest struct {
	ID           int           `json:"id"`
	Title        string        `json:"title"`
	Author       Author        `json:"author"`
	Source       Source        `json:"source"`
	Participants []Participant `json:"participants"`
}

type PullRequestsResponse struct {
	Values []PullRequest `json:"values"`
}

type TeamMember struct {
	Name      string `json:"name"`
	QueryType string `json:"query_type"` // "uuid" or "nickname"
	Query     string `json:"query"`
}

type BranchInfo struct {
	Name    string
	Author  string
	PRTitle string
	PRID    int
}

var config Config

func getConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "deploy-builder")
}

func getConfigPath() string {
	return filepath.Join(getConfigDir(), "config.json")
}

func getEnvPath() string {
	return filepath.Join(getConfigDir(), ".env")
}

func getDeploysDir() string {
	return filepath.Join(getConfigDir(), "deploys")
}

func getDeployMetadataPath(branchName string) string {
	// Sanitize branch name for filename
	safeName := strings.ReplaceAll(branchName, "/", "_")
	return filepath.Join(getDeploysDir(), safeName+".json")
}

// DeployMetadata tracks which branches were merged into a deploy branch
type DeployMetadata struct {
	DeployBranch  string            `json:"deploy_branch"`
	Branches      []string          `json:"branches"`
	BranchAuthors map[string]string `json:"branch_authors,omitempty"`
	UpdatedAt     string            `json:"updated_at"`
}

func loadDeployMetadata(deployBranch string) (*DeployMetadata, error) {
	path := getDeployMetadataPath(deployBranch)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var meta DeployMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func saveDeployMetadata(deployBranch string, branches []string, branchAuthors map[string]string) error {
	dir := getDeploysDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	meta := DeployMetadata{
		DeployBranch:  deployBranch,
		Branches:      branches,
		BranchAuthors: branchAuthors,
		UpdatedAt:     time.Now().Format(time.RFC3339),
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(getDeployMetadataPath(deployBranch), data, 0644)
}

func addBranchesToDeployMetadata(deployBranch string, newBranches []string, newAuthors map[string]string) error {
	meta, err := loadDeployMetadata(deployBranch)
	if err != nil {
		// No existing metadata, create new
		return saveDeployMetadata(deployBranch, newBranches, newAuthors)
	}

	// Add new branches, avoiding duplicates
	existing := make(map[string]bool)
	for _, b := range meta.Branches {
		existing[b] = true
	}
	for _, b := range newBranches {
		if !existing[b] {
			meta.Branches = append(meta.Branches, b)
		}
	}

	// Merge author maps
	if meta.BranchAuthors == nil {
		meta.BranchAuthors = make(map[string]string)
	}
	for branch, author := range newAuthors {
		meta.BranchAuthors[branch] = author
	}

	return saveDeployMetadata(deployBranch, meta.Branches, meta.BranchAuthors)
}

func loadEnvFile() {
	data, err := os.ReadFile(getEnvPath())
	if err != nil {
		return // .env file doesn't exist, that's ok
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			// Remove quotes if present
			value = strings.Trim(value, "\"'")
			if os.Getenv(key) == "" {
				os.Setenv(key, value)
			}
		}
	}
}

func saveEnvFile(password string) error {
	dir := getConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	content := fmt.Sprintf("BITBUCKET_API_KEY=%s\n", password)
	return os.WriteFile(getEnvPath(), []byte(content), 0600) // 0600 for security
}

func loadConfig() error {
	configPath := getConfigPath()
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &config)
}

func saveConfig() error {
	configPath := getConfigPath()

	// Create directory if it doesn't exist
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}

func configExists() bool {
	_, err := os.Stat(getConfigPath())
	return err == nil
}

// PRAuthor represents a unique PR author for selection
type PRAuthor struct {
	DisplayName string
	Nickname    string
	UUID        string
}

func fetchRecentPRAuthors(workspace, repo, username string) ([]PRAuthor, error) {
	baseURL := fmt.Sprintf("https://api.bitbucket.org/2.0/repositories/%s/%s/pullrequests", workspace, repo)

	params := url.Values{}
	params.Add("pagelen", "50")
	params.Add("fields", "values.author")

	fullURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())

	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return nil, err
	}

	bbPassword := os.Getenv("BITBUCKET_API_KEY")
	if bbPassword == "" {
		return nil, fmt.Errorf("BITBUCKET_API_KEY not set")
	}
	req.SetBasicAuth(username, bbPassword)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var response struct {
		Values []struct {
			Author struct {
				DisplayName string `json:"display_name"`
				Nickname    string `json:"nickname"`
				UUID        string `json:"uuid"`
			} `json:"author"`
		} `json:"values"`
	}

	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	// Deduplicate authors
	seen := make(map[string]bool)
	var authors []PRAuthor
	for _, pr := range response.Values {
		if !seen[pr.Author.UUID] {
			seen[pr.Author.UUID] = true
			authors = append(authors, PRAuthor{
				DisplayName: pr.Author.DisplayName,
				Nickname:    pr.Author.Nickname,
				UUID:        pr.Author.UUID,
			})
		}
	}

	return authors, nil
}

func selectTeamMembersWithFzf(authors []PRAuthor) []TeamMember {
	var fzfInput strings.Builder
	authorMap := make(map[string]PRAuthor)

	for _, a := range authors {
		line := fmt.Sprintf("%-30s │ %s", a.DisplayName, a.Nickname)
		fzfInput.WriteString(line + "\n")
		authorMap[a.DisplayName] = a
	}

	header := fmt.Sprintf("%-30s │ %s", "Name", "Nickname")
	divider := strings.Repeat("─", 50)

	cmd := exec.Command("fzf", "--multi",
		"--header="+header+"\n"+divider+"\nTAB=select  ENTER=confirm",
		"--prompt=> ",
		"--height=50%",
		"--border=rounded",
		"--ansi",
		"--pointer=>",
		"--marker=*")
	cmd.Stdin = strings.NewReader(fzfInput.String())
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var selected []TeamMember
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Extract display name (first column before │)
		parts := strings.Split(line, "│")
		if len(parts) > 0 {
			displayName := strings.TrimSpace(parts[0])
			if author, ok := authorMap[displayName]; ok {
				// Use UUID for more reliable matching
				selected = append(selected, TeamMember{
					Name:      author.DisplayName,
					QueryType: "uuid",
					Query:     author.UUID,
				})
			}
		}
	}

	return selected
}

func runSetup() {
	fmt.Printf("\n%s%s Deploy Builder Setup %s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s%s\n\n", Dim, strings.Repeat("─", 50), Reset)

	if configExists() {
		fmt.Printf("%sExisting configuration found.%s\n", Yellow, Reset)
		if !promptYesNo("Overwrite existing config?") {
			fmt.Println("Setup cancelled.")
			return
		}
		fmt.Println()
	}

	// Bitbucket settings
	fmt.Printf("%sBitbucket Settings%s\n", Bold, Reset)
	fmt.Printf("%s(Find these in your repo URL: bitbucket.org/{workspace}/{repo})%s\n\n", Dim, Reset)

	config.Workspace = prompt("Workspace: ")
	config.RepoSlug = prompt("Repository slug: ")
	config.Username = prompt("Your Bitbucket username: ")

	// Check for API password
	if os.Getenv("BITBUCKET_API_KEY") == "" {
		fmt.Printf("\n%sBitbucket API Key%s\n", Bold, Reset)
		fmt.Printf("%sCreate one at: https://bitbucket.org/account/settings/api-tokens/%s\n", Dim, Reset)
		fmt.Printf("%sRequired permissions: Repositories (Read), Pull requests (Read)%s\n\n", Dim, Reset)
		password := prompt("API key: ")
		if password != "" {
			if err := saveEnvFile(password); err != nil {
				fmt.Printf("%sWarning: Could not save password: %v%s\n", Yellow, err, Reset)
			} else {
				os.Setenv("BITBUCKET_API_KEY", password)
				fmt.Printf("%s✓ Password saved to %s%s\n", Green, getEnvPath(), Reset)
			}
		}
	} else {
		fmt.Printf("\n%s✓ Bitbucket API key found%s\n", Green, Reset)
	}

	// Team members - try to fetch from repo
	fmt.Printf("\n%sTeam Members%s\n", Bold, Reset)
	fmt.Printf("Fetching recent PR authors from %s/%s...\n", config.Workspace, config.RepoSlug)

	authors, err := fetchRecentPRAuthors(config.Workspace, config.RepoSlug, config.Username)
	if err != nil {
		fmt.Printf("%sWarning: Could not fetch authors: %v%s\n", Yellow, err, Reset)
		fmt.Println("You can add team members manually by editing the config file.")
		config.Team = []TeamMember{}
	} else if len(authors) == 0 {
		fmt.Printf("%sNo PR authors found in the repository.%s\n", Yellow, Reset)
		config.Team = []TeamMember{}
	} else {
		fmt.Printf("Found %d unique authors. Select your team members:\n\n", len(authors))
		config.Team = selectTeamMembersWithFzf(authors)

		if len(config.Team) == 0 {
			fmt.Printf("%sNo team members selected.%s\n", Yellow, Reset)
		} else {
			fmt.Printf("\n%s✓ Selected %d team members:%s\n", Green, len(config.Team), Reset)
			for _, m := range config.Team {
				fmt.Printf("  • %s\n", m.Name)
			}
		}
	}

	if len(config.Team) == 0 {
		fmt.Printf("\n%sYou can edit the config later at: %s%s\n", Dim, getConfigPath(), Reset)
	}

	// Preview servers (optional)
	fmt.Printf("\n%sPreview Servers (Optional)%s\n", Bold, Reset)
	fmt.Printf("%sAdd commands to deploy to preview servers.%s\n", Dim, Reset)
	fmt.Printf("%sUse {branch} as placeholder for the branch name.%s\n\n", Dim, Reset)

	config.PreviewServers = []PreviewServer{}
	if promptYesNo("Add preview servers?") {
		for {
			name := prompt("\nServer name (empty to finish): ")
			if name == "" {
				break
			}

			fmt.Printf("Deploy command (use {branch} for branch name)\n")
			command := prompt("Command: ")

			config.PreviewServers = append(config.PreviewServers, PreviewServer{
				Name:    name,
				Command: command,
			})
			fmt.Printf("%s✓ Added server '%s'%s\n", Green, name, Reset)
		}
	}

	// Save config
	if err := saveConfig(); err != nil {
		fmt.Printf("%sError saving config: %v%s\n", Red, err, Reset)
		os.Exit(1)
	}

	fmt.Printf("\n%s✓ Configuration saved to %s%s\n", Green, getConfigPath(), Reset)
}

func getPRsForMember(member TeamMember) ([]PullRequest, error) {
	baseURL := fmt.Sprintf("https://api.bitbucket.org/2.0/repositories/%s/%s/pullrequests",
		config.Workspace, config.RepoSlug)

	params := url.Values{}
	if member.QueryType == "uuid" {
		params.Add("q", fmt.Sprintf(`state="OPEN" AND author.uuid="%s"`, member.Query))
	} else {
		params.Add("q", fmt.Sprintf(`state="OPEN" AND author.nickname="%s"`, member.Query))
	}
	params.Add("pagelen", "50")
	params.Add("fields", "values.id,values.title,values.author,values.source,values.participants")

	fullURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())

	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return nil, err
	}

	bbPassword := os.Getenv("BITBUCKET_API_KEY")
	if bbPassword == "" {
		fmt.Printf("%sError: BITBUCKET_API_KEY environment variable not set%s\n", Red, Reset)
		fmt.Println("Add to your shell config:")
		fmt.Println("  export BITBUCKET_API_KEY=\"your-api-key\"")
		os.Exit(1)
	}
	req.SetBasicAuth(config.Username, bbPassword)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var prResponse PullRequestsResponse
	if err := json.Unmarshal(body, &prResponse); err != nil {
		return nil, err
	}

	return prResponse.Values, nil
}

func fetchAllTeamBranches() ([]BranchInfo, map[string][]PullRequest, error) {
	var branches []BranchInfo
	prsByMember := make(map[string][]PullRequest)

	for _, member := range config.Team {
		prs, err := getPRsForMember(member)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%sWarning: Error fetching PRs for %s: %v%s\n", Yellow, member.Name, err, Reset)
			continue
		}

		prsByMember[member.Name] = prs

		for _, pr := range prs {
			branches = append(branches, BranchInfo{
				Name:    pr.Source.Branch.Name,
				Author:  member.Name,
				PRTitle: pr.Title,
				PRID:    pr.ID,
			})
		}
	}

	return branches, prsByMember, nil
}

func displayPROverview(prsByMember map[string][]PullRequest) {
	fmt.Printf("\n%s%s Open PRs by Team %s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s%s\n", Dim, strings.Repeat("─", 70), Reset)

	totalPRs := 0
	for _, member := range config.Team {
		prs := prsByMember[member.Name]
		fmt.Printf("\n%s%s%s %s(%d PRs)%s\n", Bold, Yellow, member.Name, Dim, len(prs), Reset)

		if len(prs) == 0 {
			fmt.Printf("  %sNo open pull requests%s\n", Dim, Reset)
			continue
		}

		for _, pr := range prs {
			totalPRs++
			prNum := fmt.Sprintf("#%d", pr.ID)
			title := truncate(pr.Title, 45)

			approvalCount, approvers := getApprovalStatus(pr.Participants)
			var approvalStr string
			if approvalCount > 0 {
				approverNames := strings.Join(approvers, ", ")
				approvalStr = fmt.Sprintf("%s✓ %d (%s)%s", Green, approvalCount, approverNames, Reset)
			} else {
				approvalStr = fmt.Sprintf("%s○ No approvals%s", Red, Reset)
			}

			fmt.Printf("  %s%-6s%s %s\n", Green, prNum, Reset, title)
			fmt.Printf("         %s%s%s  %s\n", Dim, truncate(pr.Source.Branch.Name, 50), Reset, approvalStr)
		}
	}

	fmt.Printf("\n%s%s%s\n", Dim, strings.Repeat("─", 70), Reset)
	fmt.Printf("%sTotal: %d open PRs%s\n\n", Bold, totalPRs, Reset)
}

func getApprovalStatus(participants []Participant) (int, []string) {
	approvalCount := 0
	var approvers []string
	for _, p := range participants {
		if p.Approved {
			approvalCount++
			approvers = append(approvers, p.User.DisplayName)
		}
	}
	return approvalCount, approvers
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func runCommandSilent(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// runCommandQuiet runs a command silently, only showing output on error
func runCommandQuiet(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Show the output only on failure
		fmt.Printf("\n%sCommand failed: %s %s%s\n", Red, name, strings.Join(args, " "), Reset)
		fmt.Printf("%s%s%s\n", Dim, string(output), Reset)
	}
	return err
}

func checkGitConflicts() bool {
	output, _ := runCommandSilent("git", "status", "--porcelain")
	return strings.Contains(output, "UU") || strings.Contains(output, "AA") || strings.Contains(output, "DD")
}

func getCurrentBranch() string {
	output, _ := runCommandSilent("git", "rev-parse", "--abbrev-ref", "HEAD")
	return strings.TrimSpace(output)
}

func isDeployBranch(branchName string) bool {
	// Match pattern like 20241201-something or 20241201-deploy
	matched, _ := regexp.MatchString(`^\d{8}-`, branchName)
	return matched
}

func getMergedBranchesFromLog() []string {
	// Parse git log for merge commits like "Merge origin/branch-name into deploy"
	output, err := runCommandSilent("git", "log", "--oneline", "--merges", "-50")
	if err != nil {
		return nil
	}

	var branches []string
	seen := make(map[string]bool)
	re := regexp.MustCompile(`Merge origin/([^\s]+) into`)

	for _, line := range strings.Split(output, "\n") {
		matches := re.FindStringSubmatch(line)
		if len(matches) > 1 {
			branch := matches[1]
			if !seen[branch] && branch != "master" {
				seen[branch] = true
				branches = append(branches, branch)
			}
		}
	}

	return branches
}

func prompt(message string) string {
	fmt.Print(message)
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	return strings.TrimSpace(input)
}

func promptWithDefault(message string, defaultValue string) string {
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          message,
		InterruptPrompt: "^C",
	})
	if err != nil {
		// Fallback to simple prompt
		return prompt(message)
	}
	defer rl.Close()

	// Set the initial buffer with the default value
	rl.WriteStdin([]byte(defaultValue))

	line, err := rl.Readline()
	if err != nil {
		return defaultValue
	}
	return strings.TrimSpace(line)
}

func promptYesNo(message string) bool {
	response := prompt(message + " (y/n): ")
	return strings.ToLower(response) == "y" || strings.ToLower(response) == "yes"
}

func selectBranchesWithFzf(branches []BranchInfo) ([]BranchInfo, error) {
	// Build fzf input with fixed-width columns
	// Truncate branch names to 40 chars to keep alignment
	var fzfInput strings.Builder
	branchMap := make(map[string]BranchInfo)

	for _, b := range branches {
		truncatedName := truncate(b.Name, 40)
		// Use fixed widths: branch (40), author (10), PR# (7), title (remaining)
		line := fmt.Sprintf("%-40s │ %-8s │ #%-5d │ %s", truncatedName, b.Author, b.PRID, truncate(b.PRTitle, 35))
		fzfInput.WriteString(line + "\n")
		branchMap[truncatedName] = b
		// Also map full name in case truncation doesn't happen
		branchMap[b.Name] = b
	}

	header := fmt.Sprintf("%-40s │ %-8s │ %-7s │ %s", "Branch", "Author", "PR#", "Title")
	divider := strings.Repeat("─", 80)

	cmd := exec.Command("fzf", "--multi",
		"--header="+header+"\n"+divider+"\nTAB=select  ENTER=confirm",
		"--prompt=> ",
		"--height=50%",
		"--border=rounded",
		"--ansi",
		"--pointer=>",
		"--marker=*")
	cmd.Stdin = strings.NewReader(fzfInput.String())
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 130 {
			return nil, fmt.Errorf("selection cancelled")
		}
		return nil, err
	}

	// Parse selected branches
	var selected []BranchInfo
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Extract branch name (first column before │)
		parts := strings.Split(line, "│")
		if len(parts) > 0 {
			branchName := strings.TrimSpace(parts[0])
			if info, ok := branchMap[branchName]; ok {
				selected = append(selected, info)
			}
		}
	}

	return selected, nil
}

func selectPreviewServer() string {
	if len(config.PreviewServers) == 0 {
		return "skip"
	}

	var options strings.Builder
	for i, server := range config.PreviewServers {
		options.WriteString(fmt.Sprintf("[%d]  %s\n", i+1, server.Name))
	}
	options.WriteString("[x]  skip - don't deploy now")

	cmd := exec.Command("fzf",
		"--header=Select preview server:",
		"--prompt=> ",
		"--height=~10",
		"--border=rounded",
		"--ansi",
		"--no-info")
	cmd.Stdin = strings.NewReader(options.String())
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		return "skip"
	}

	result := strings.TrimSpace(string(output))
	if strings.Contains(result, "skip") {
		return "skip"
	}

	// Find which server was selected
	for _, server := range config.PreviewServers {
		if strings.Contains(result, server.Name) {
			return server.Name
		}
	}
	return "skip"
}

func selectMode() string {
	options := "[+]  Create new deploy branch\n[~]  Resync existing deploy branch"

	cmd := exec.Command("fzf",
		"--header=What would you like to do?",
		"--prompt=> ",
		"--height=~10",
		"--border=rounded",
		"--ansi",
		"--no-info")
	cmd.Stdin = strings.NewReader(options)
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		return "create"
	}

	if strings.Contains(string(output), "Resync") {
		return "resync"
	}
	return "create"
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// getHeadCommit returns the current HEAD commit hash
func getHeadCommit() string {
	output, _ := runCommandSilent("git", "rev-parse", "HEAD")
	return strings.TrimSpace(output)
}

// buildAuthorBranchMap builds a map of author -> []branches from BranchInfo slice
func buildAuthorBranchMap(infos []BranchInfo) map[string][]string {
	result := make(map[string][]string)
	for _, info := range infos {
		result[info.Author] = append(result[info.Author], info.Name)
	}
	return result
}

// buildBranchAuthorMap builds a map of branch -> author from BranchInfo slice
func buildBranchAuthorMap(infos []BranchInfo) map[string]string {
	result := make(map[string]string)
	for _, info := range infos {
		result[info.Name] = info.Author
	}
	return result
}

// lookupAuthorsForBranches resolves authors for branch names using available branch info and metadata
func lookupAuthorsForBranches(branchNames []string, allBranches []BranchInfo, meta *DeployMetadata) map[string][]string {
	// Build lookup from available branch info
	branchToAuthor := make(map[string]string)
	for _, b := range allBranches {
		branchToAuthor[b.Name] = b.Author
	}
	// Also use metadata authors as fallback
	if meta != nil && meta.BranchAuthors != nil {
		for branch, author := range meta.BranchAuthors {
			if _, exists := branchToAuthor[branch]; !exists {
				branchToAuthor[branch] = author
			}
		}
	}

	result := make(map[string][]string)
	for _, name := range branchNames {
		author := branchToAuthor[name]
		if author == "" {
			author = "Unknown"
		}
		result[author] = append(result[author], name)
	}
	return result
}

func syncAndMergeBranch(branch string, deployBranch string) error {
	fmt.Printf("%sProcessing: %s%s\n", Cyan, branch, Reset)

	// Checkout the feature branch
	fmt.Printf("  Checking out...")
	if err := runCommandQuiet("git", "checkout", branch); err != nil {
		// Branch might not exist locally, try to fetch it
		if err := runCommandQuiet("git", "checkout", "-b", branch, "origin/"+branch); err != nil {
			// Try just checking out from origin
			if err := runCommandQuiet("git", "checkout", "--track", "origin/"+branch); err != nil {
				fmt.Printf(" %s✗%s\n", Red, Reset)
				return fmt.Errorf("error checking out branch: %v", err)
			}
		}
	}
	fmt.Printf(" %s✓%s\n", Green, Reset)

	// Pull latest from feature branch
	fmt.Printf("  Pulling latest...")
	headBefore := getHeadCommit()
	runCommandQuiet("git", "pull", "origin", branch)
	headAfter := getHeadCommit()
	if headBefore != headAfter {
		fmt.Printf(" %s✓↓%s\n", Green, Reset)
	} else {
		fmt.Printf(" %s✓%s\n", Green, Reset)
	}

	// Merge origin/master into feature branch
	fmt.Printf("  Syncing with master...")
	headBefore = getHeadCommit()
	if err := runCommandQuiet("git", "merge", "origin/master", "-m", fmt.Sprintf("Merge origin/master into %s", branch)); err != nil {
		if checkGitConflicts() {
			fmt.Printf(" %s✗%s\n", Red, Reset)
			fmt.Printf("\n%s╔════════════════════════════════════════════════════════════════╗%s\n", Red, Reset)
			fmt.Printf("%s║  MERGE CONFLICT while syncing with master                      ║%s\n", Red, Reset)
			fmt.Printf("%s║  Branch: %-52s ║%s\n", Red, truncate(branch, 52), Reset)
			fmt.Printf("%s║  Please resolve conflicts manually, then re-run this tool.     ║%s\n", Red, Reset)
			fmt.Printf("%s╚════════════════════════════════════════════════════════════════╝%s\n", Red, Reset)
			return fmt.Errorf("merge conflict")
		}
	}
	headAfter = getHeadCommit()
	if headBefore != headAfter {
		fmt.Printf(" %s✓↓%s\n", Green, Reset)
	} else {
		fmt.Printf(" %s✓%s\n", Green, Reset)
	}

	// Push synced feature branch to origin
	fmt.Printf("  Pushing to origin...")
	output, err := runCommandSilent("git", "push", "origin", branch)
	if err != nil {
		fmt.Printf(" %s⚠%s %s(could not push, continuing)%s\n", Yellow, Reset, Dim, Reset)
	} else if strings.Contains(output, "Everything up-to-date") {
		fmt.Printf(" %s✓%s\n", Green, Reset)
	} else {
		fmt.Printf(" %s✓↑%s\n", Green, Reset)
	}

	// Return to deploy branch
	fmt.Printf("  Returning to deploy...")
	if err := runCommandQuiet("git", "checkout", deployBranch); err != nil {
		fmt.Printf(" %s✗%s\n", Red, Reset)
		return fmt.Errorf("error returning to deploy branch: %v", err)
	}
	fmt.Printf(" %s✓%s\n", Green, Reset)

	// Merge origin/feature-branch into deploy branch
	fmt.Printf("  Merging into deploy...")
	headBefore = getHeadCommit()
	if err := runCommandQuiet("git", "merge", "origin/"+branch, "-m", fmt.Sprintf("Merge origin/%s into deploy", branch)); err != nil {
		if checkGitConflicts() {
			fmt.Printf(" %s✗%s\n", Red, Reset)
			fmt.Printf("\n%s╔════════════════════════════════════════════════════════════════╗%s\n", Red, Reset)
			fmt.Printf("%s║  MERGE CONFLICT while merging into deploy                      ║%s\n", Red, Reset)
			fmt.Printf("%s║  Branch: %-52s ║%s\n", Red, truncate(branch, 52), Reset)
			fmt.Printf("%s║  Please resolve conflicts manually, then re-run this tool.     ║%s\n", Red, Reset)
			fmt.Printf("%s╚════════════════════════════════════════════════════════════════╝%s\n", Red, Reset)
			return fmt.Errorf("merge conflict")
		}
	}
	headAfter = getHeadCommit()
	if headBefore != headAfter {
		fmt.Printf(" %s✓↓%s\n", Green, Reset)
	} else {
		fmt.Printf(" %s✓%s\n", Green, Reset)
	}

	return nil
}

func resyncMode(deployBranch string, branches []BranchInfo) {
	fmt.Printf("\n%s%s Resync Mode %s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s%s\n", Dim, strings.Repeat("─", 50), Reset)
	fmt.Printf("Deploy branch: %s%s%s\n", Yellow, deployBranch, Reset)

	// Get previously merged branches from metadata
	var mergedBranches []string
	meta, err := loadDeployMetadata(deployBranch)
	if err == nil {
		mergedBranches = meta.Branches
	}

	if len(mergedBranches) == 0 {
		fmt.Printf("%sNo previously merged branches found in this deploy branch.%s\n", Yellow, Reset)
		fmt.Printf("Would you like to select branches to add?\n")
		if !promptYesNo("Select new branches?") {
			return
		}
		// Fall through to selection
	} else {
		fmt.Printf("\n%sPreviously merged branches:%s\n", Bold, Reset)
		for _, b := range mergedBranches {
			fmt.Printf("  • %s\n", b)
		}

		fmt.Printf("\n%sOptions:%s\n", Bold, Reset)
		options := "[*]  Resync all   - re-sync all previously merged branches\n[?]  Select some  - choose specific branches to resync\n[+]  Add new      - add additional branches to this deploy"

		cmd := exec.Command("fzf",
			"--header=What would you like to do?",
			"--prompt=> ",
			"--height=~10",
			"--border=rounded",
			"--ansi",
			"--no-info")
		cmd.Stdin = strings.NewReader(options)
		cmd.Stderr = os.Stderr

		output, err := cmd.Output()
		if err != nil {
			fmt.Println("Cancelled.")
			return
		}

		choice := strings.TrimSpace(string(output))

		if strings.Contains(choice, "Resync all") || strings.Contains(choice, "Re-sync all") {
			// Resync all previously merged branches
			fmt.Printf("\n%sFetching latest from origin...%s", Bold, Reset)
			if err := runCommandQuiet("git", "fetch", "origin"); err != nil {
				fmt.Printf(" %s✗%s\n", Red, Reset)
				return
			}
			fmt.Printf(" %s✓%s\n", Green, Reset)

			fmt.Printf("\n%sResyncing %d branches...%s\n", Bold, len(mergedBranches), Reset)
			for i, branch := range mergedBranches {
				fmt.Printf("\n%s[%d/%d]%s ", Cyan, i+1, len(mergedBranches), Reset)
				if err := syncAndMergeBranch(branch, deployBranch); err != nil {
					return
				}
			}

			authorMap := lookupAuthorsForBranches(mergedBranches, branches, meta)
			showCompletionSummary(deployBranch, authorMap)
			offerPreviewDeploy(deployBranch)
			return
		} else if strings.Contains(choice, "Select specific") || strings.Contains(choice, "Select some") {
			// Let user select which of the merged branches to resync
			var branchInfos []BranchInfo
			for _, b := range mergedBranches {
				branchInfos = append(branchInfos, BranchInfo{Name: b, Author: "Previously merged"})
			}

			selected, err := selectBranchesWithFzf(branchInfos)
			if err != nil || len(selected) == 0 {
				fmt.Println("No branches selected.")
				return
			}

			fmt.Printf("\n%sFetching latest from origin...%s", Bold, Reset)
			runCommandQuiet("git", "fetch", "origin")
			fmt.Printf(" %s✓%s\n", Green, Reset)

			var branchNames []string
			for i, b := range selected {
				fmt.Printf("\n%s[%d/%d]%s ", Cyan, i+1, len(selected), Reset)
				if err := syncAndMergeBranch(b.Name, deployBranch); err != nil {
					return
				}
				branchNames = append(branchNames, b.Name)
			}

			authorMap := lookupAuthorsForBranches(branchNames, branches, meta)
			showCompletionSummary(deployBranch, authorMap)
			offerPreviewDeploy(deployBranch)
			return
		}
		// Fall through to "Add new branches"
	}

	// Add new branches - show fzf with all team PRs
	fmt.Printf("\n%sSelect additional branches to add:%s\n", Bold, Reset)
	selected, err := selectBranchesWithFzf(branches)
	if err != nil || len(selected) == 0 {
		fmt.Println("No branches selected.")
		return
	}

	fmt.Printf("\n%sFetching latest from origin...%s", Bold, Reset)
	runCommandQuiet("git", "fetch", "origin")
	fmt.Printf(" %s✓%s\n", Green, Reset)

	var branchNames []string
	for i, b := range selected {
		fmt.Printf("\n%s[%d/%d]%s ", Cyan, i+1, len(selected), Reset)
		if err := syncAndMergeBranch(b.Name, deployBranch); err != nil {
			return
		}
		branchNames = append(branchNames, b.Name)
	}

	// Save newly added branches to metadata
	newAuthors := buildBranchAuthorMap(selected)
	addBranchesToDeployMetadata(deployBranch, branchNames, newAuthors)

	authorMap := buildAuthorBranchMap(selected)
	showCompletionSummary(deployBranch, authorMap)
	offerPreviewDeploy(deployBranch)
}

func showCompletionSummary(deployBranch string, authorBranches map[string][]string) {
	fmt.Printf("\n%s%s%s\n", Dim, strings.Repeat("─", 50), Reset)
	fmt.Printf("%s✓ Deploy branch '%s' updated!%s\n\n", Green, deployBranch, Reset)

	fmt.Printf("%sBranches included:%s\n", Bold, Reset)

	var authors []string
	for author := range authorBranches {
		authors = append(authors, author)
	}
	sort.Strings(authors)

	for _, author := range authors {
		fmt.Printf("\n%s%s%s\n", Yellow, author, Reset)
		for _, b := range authorBranches[author] {
			fmt.Printf("- %s\n", b)
		}
	}
}

func pushDeployBranch(deployBranch string) bool {
	fmt.Printf("\nPushing deploy branch to origin...")
	if err := runCommandQuiet("git", "push", "-u", "origin", deployBranch); err != nil {
		fmt.Printf(" %s✗%s\n", Red, Reset)
		return false
	}
	fmt.Printf(" %s✓%s\n", Green, Reset)
	return true
}

func offerPreviewDeploy(deployBranch string) {
	// Always push to origin first
	if !pushDeployBranch(deployBranch) {
		return
	}

	if len(config.PreviewServers) == 0 {
		fmt.Printf("\n%s%sDone!%s\n\n", Bold, Green, Reset)
		return
	}

	fmt.Printf("\n%sDeploy to preview server?%s\n", Bold, Reset)
	serverName := selectPreviewServer()

	if serverName != "skip" {
		// Find the server config
		var server PreviewServer
		for _, s := range config.PreviewServers {
			if s.Name == serverName {
				server = s
				break
			}
		}

		fmt.Printf("\nDeploying to %s%s%s preview server...\n", Cyan, serverName, Reset)
		fmt.Printf("%sOutput shown below:%s\n", Dim, Reset)
		fmt.Printf("%s%s%s\n", Dim, strings.Repeat("─", 50), Reset)

		// Replace {branch} placeholder with actual branch name
		deployCmd := strings.ReplaceAll(server.Command, "{branch}", deployBranch)

		// Run the deploy command
		if err := runCommand("sh", "-c", deployCmd); err != nil {
			fmt.Printf("%sWarning: Deploy command may have failed: %v%s\n", Yellow, err, Reset)
		}
		fmt.Printf("%s%s%s\n", Dim, strings.Repeat("─", 50), Reset)
	}

	fmt.Printf("\n%s%sDone!%s\n\n", Bold, Green, Reset)
}

func createMode(branches []BranchInfo, prsByMember map[string][]PullRequest) {
	// Step 1: Show PR overview first
	fmt.Printf("\n%sStep 1: Current Team PRs%s\n", Bold, Reset)
	displayPROverview(prsByMember)

	// Step 2: Get deploy branch name
	today := time.Now().Format("20060102")
	datePrefix := today + "-"
	fmt.Printf("%sStep 2: Deploy Branch Name%s\n", Bold, Reset)
	fmt.Printf("Format: %s<description> (e.g., %smy-feature)\n", datePrefix, datePrefix)
	branchName := promptWithDefault("Branch name: ", datePrefix)
	if branchName == "" || branchName == datePrefix {
		branchName = datePrefix + "deploy"
	}

	// Step 3: Fetch and update origin/master
	fmt.Printf("\n%sStep 3: Fetching latest from origin...%s", Bold, Reset)
	if err := runCommandQuiet("git", "fetch", "origin"); err != nil {
		fmt.Printf(" %s✗%s\n", Red, Reset)
		fmt.Printf("%sError fetching from origin: %v%s\n", Red, err, Reset)
		os.Exit(1)
	}
	fmt.Printf(" %s✓%s\n", Green, Reset)

	// Step 4: Create deploy branch from origin/master
	fmt.Printf("\n%sStep 4: Creating deploy branch from origin/master...%s", Bold, Reset)
	if err := runCommandQuiet("git", "checkout", "-b", branchName, "origin/master"); err != nil {
		fmt.Printf(" %s✗%s\n", Red, Reset)
		fmt.Println("The branch may already exist. Try a different name.")
		os.Exit(1)
	}
	fmt.Printf(" %s✓%s\n", Green, Reset)
	fmt.Printf("  Branch: %s%s%s\n", Cyan, branchName, Reset)

	// Step 5: Select branches with fzf
	fmt.Printf("\n%sStep 5: Select branches to include%s\n", Bold, Reset)
	selectedBranches, err := selectBranchesWithFzf(branches)
	if err != nil {
		fmt.Printf("%sSelection cancelled%s\n", Yellow, Reset)
		runCommandQuiet("git", "checkout", "master")
		runCommandQuiet("git", "branch", "-D", branchName)
		os.Exit(1)
	}

	if len(selectedBranches) == 0 {
		fmt.Printf("%sNo branches selected%s\n", Yellow, Reset)
		runCommandQuiet("git", "checkout", "master")
		runCommandQuiet("git", "branch", "-D", branchName)
		os.Exit(0)
	}

	fmt.Printf("\n%sSelected %d branches:%s\n", Green, len(selectedBranches), Reset)
	for _, b := range selectedBranches {
		fmt.Printf("  • %s (%s)\n", b.Name, b.Author)
	}

	if !promptYesNo("\nProceed with merging these branches?") {
		runCommandQuiet("git", "checkout", "master")
		runCommandQuiet("git", "branch", "-D", branchName)
		fmt.Println("Cancelled.")
		os.Exit(0)
	}

	// Step 6: Process each selected branch
	fmt.Printf("\n%sStep 6: Processing branches...%s\n", Bold, Reset)
	mergedBranches := make(map[string][]string) // author -> branches
	var allBranchNames []string
	for _, branch := range selectedBranches {
		allBranchNames = append(allBranchNames, branch.Name)
	}

	// Save metadata now so branches are remembered if a merge fails
	branchAuthors := buildBranchAuthorMap(selectedBranches)
	saveDeployMetadata(branchName, allBranchNames, branchAuthors)

	for i, branch := range selectedBranches {
		fmt.Printf("\n%s[%d/%d]%s ", Cyan, i+1, len(selectedBranches), Reset)
		if err := syncAndMergeBranch(branch.Name, branchName); err != nil {
			os.Exit(1)
		}
		mergedBranches[branch.Author] = append(mergedBranches[branch.Author], branch.Name)
	}

	// Step 7: Summary
	showCompletionSummary(branchName, mergedBranches)

	// Step 8: Deploy to preview
	fmt.Printf("\n%sStep 7: Deploy to preview server?%s\n", Bold, Reset)
	offerPreviewDeploy(branchName)
}

func editTeamMembers() {
	fmt.Printf("\n%s%s Edit Team Members %s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s%s\n\n", Dim, strings.Repeat("─", 50), Reset)

	if !configExists() {
		fmt.Printf("%sNo configuration found. Run 'deploy-builder --setup' first.%s\n", Yellow, Reset)
		return
	}

	if err := loadConfig(); err != nil {
		fmt.Printf("%sError loading config: %v%s\n", Red, err, Reset)
		return
	}

	// Load env for API access
	loadEnvFile()

	if len(config.Team) > 0 {
		fmt.Printf("%sCurrent team members:%s\n", Bold, Reset)
		for _, m := range config.Team {
			fmt.Printf("  • %s\n", m.Name)
		}
		fmt.Println()
	}

	fmt.Printf("Fetching PR authors from %s/%s...\n", config.Workspace, config.RepoSlug)

	authors, err := fetchRecentPRAuthors(config.Workspace, config.RepoSlug, config.Username)
	if err != nil {
		fmt.Printf("%sError fetching authors: %v%s\n", Red, err, Reset)
		return
	}

	if len(authors) == 0 {
		fmt.Printf("%sNo PR authors found.%s\n", Yellow, Reset)
		return
	}

	fmt.Printf("Found %d authors. Select your team members:\n\n", len(authors))
	config.Team = selectTeamMembersWithFzf(authors)

	if len(config.Team) == 0 {
		fmt.Printf("%sNo team members selected.%s\n", Yellow, Reset)
		return
	}

	if err := saveConfig(); err != nil {
		fmt.Printf("%sError saving config: %v%s\n", Red, err, Reset)
		return
	}

	fmt.Printf("\n%s✓ Updated team members:%s\n", Green, Reset)
	for _, m := range config.Team {
		fmt.Printf("  • %s\n", m.Name)
	}
	fmt.Println()
}

func printUsage() {
	fmt.Printf("\n%s%s Deploy Branch Builder %s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s%s\n\n", Dim, strings.Repeat("─", 50), Reset)
	fmt.Println("Usage: deploy-builder [options]")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --setup    Run configuration setup")
	fmt.Println("  --team     Edit team members")
	fmt.Println("  --config   Show current configuration")
	fmt.Println("  --help     Show this help message")
	fmt.Println()
}

func showConfig() {
	if !configExists() {
		fmt.Printf("%sNo configuration found. Run 'deploy-builder --setup' to create one.%s\n", Yellow, Reset)
		return
	}

	if err := loadConfig(); err != nil {
		fmt.Printf("%sError loading config: %v%s\n", Red, err, Reset)
		return
	}

	fmt.Printf("\n%s%s Current Configuration %s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s%s\n\n", Dim, strings.Repeat("─", 50), Reset)

	fmt.Printf("%sConfig file:%s %s\n\n", Bold, Reset, getConfigPath())

	fmt.Printf("%sBitbucket:%s\n", Bold, Reset)
	fmt.Printf("  Workspace: %s\n", config.Workspace)
	fmt.Printf("  Repository: %s\n", config.RepoSlug)
	fmt.Printf("  Username: %s\n", config.Username)

	fmt.Printf("\n%sTeam Members:%s\n", Bold, Reset)
	for _, m := range config.Team {
		fmt.Printf("  • %s (%s: %s)\n", m.Name, m.QueryType, m.Query)
	}

	if len(config.PreviewServers) > 0 {
		fmt.Printf("\n%sPreview Servers:%s\n", Bold, Reset)
		for _, s := range config.PreviewServers {
			fmt.Printf("  • %s: %s\n", s.Name, s.Command)
		}
	}
	fmt.Println()
}

func main() {
	// Load .env file if it exists
	loadEnvFile()

	// Parse command line arguments
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--setup":
			runSetup()
			return
		case "--team":
			editTeamMembers()
			return
		case "--config":
			showConfig()
			return
		case "--help", "-h":
			printUsage()
			return
		}
	}

	fmt.Printf("\n%s%s Deploy Branch Builder %s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s%s\n", Dim, strings.Repeat("─", 50), Reset)

	// Check for config
	if !configExists() {
		fmt.Printf("%sNo configuration found.%s\n\n", Yellow, Reset)
		fmt.Println("Run initial setup to configure the tool.")
		if promptYesNo("Run setup now?") {
			runSetup()
			fmt.Println("\nConfiguration complete. Run deploy-builder again to start.")
		}
		return
	}

	// Load config
	if err := loadConfig(); err != nil {
		fmt.Printf("%sError loading config: %v%s\n", Red, err, Reset)
		fmt.Println("Run 'deploy-builder --setup' to reconfigure.")
		os.Exit(1)
	}

	// Check we're in a git repo
	if _, err := runCommandSilent("git", "rev-parse", "--show-toplevel"); err != nil {
		fmt.Printf("%sError: Not in a git repository%s\n", Red, Reset)
		os.Exit(1)
	}

	// Fetch team PRs first (needed for both modes)
	fmt.Printf("Fetching team PRs from Bitbucket...\n")
	branches, prsByMember, err := fetchAllTeamBranches()
	if err != nil {
		fmt.Printf("%sError fetching PRs: %v%s\n", Red, err, Reset)
		os.Exit(1)
	}

	// Check if we're on a deploy branch
	currentBranch := getCurrentBranch()
	if isDeployBranch(currentBranch) {
		fmt.Printf("\n%sDetected deploy branch: %s%s\n", Yellow, currentBranch, Reset)
		mode := selectMode()

		if mode == "resync" {
			resyncMode(currentBranch, branches)
			return
		}
		// Otherwise fall through to create mode
	}

	if len(branches) == 0 {
		fmt.Printf("%sNo open PRs found for team members%s\n", Yellow, Reset)
		os.Exit(0)
	}

	fmt.Printf("Found %d open PRs\n", len(branches))

	createMode(branches, prsByMember)
}
