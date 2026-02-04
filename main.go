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
	Name      string
	QueryType string // "uuid" or "nickname"
	Query     string
}

type BranchInfo struct {
	Name    string
	Author  string
	PRTitle string
	PRID    int
}

var team = []TeamMember{
	{Name: "Jackson", QueryType: "uuid", Query: "{a2270a50-355b-45d0-9fd0-cc60113cffc3}"},
	{Name: "Nelson", QueryType: "nickname", Query: "Nelson Solano"},
	{Name: "Justin", QueryType: "nickname", Query: "Justin Andersen"},
	{Name: "Joe", QueryType: "nickname", Query: "Joe Busigin"},
}

func getPRsForMember(member TeamMember) ([]PullRequest, error) {
	baseURL := "https://api.bitbucket.org/2.0/repositories/WeBuyCars/osg/pullrequests"

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

	bbPassword := os.Getenv("BITBUCKET_APP_PASSWORD")
	if bbPassword == "" {
		fmt.Printf("%sError: BITBUCKET_APP_PASSWORD environment variable not set%s\n", Red, Reset)
		fmt.Println("Add to your ~/.zshrc:")
		fmt.Println("  export BITBUCKET_APP_PASSWORD=\"your-app-password\"")
		os.Exit(1)
	}
	req.SetBasicAuth("jkurtzgps", bbPassword)
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

	for _, member := range team {
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
	for _, member := range team {
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
	options := "[1]  history   - preview-ui-history server\n[2]  feedback  - preview-feedback server\n[x]  skip      - don't deploy now"

	cmd := exec.Command("fzf",
		"--header=Select preview server:",
		"--prompt=> ",
		"--height=~10",
		"--border=rounded",
		"--ansi",
		"--no-info")
	cmd.Stdin = strings.NewReader(options)
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		return "skip"
	}

	result := strings.TrimSpace(string(output))
	if strings.Contains(result, "history") {
		return "history"
	} else if strings.Contains(result, "feedback") {
		return "feedback"
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
	runCommandQuiet("git", "pull", "origin", branch)
	fmt.Printf(" %s✓%s\n", Green, Reset)

	// Merge origin/master into feature branch
	fmt.Printf("  Syncing with master...")
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
	fmt.Printf(" %s✓%s\n", Green, Reset)

	// Push synced feature branch to origin
	fmt.Printf("  Pushing to origin...")
	if err := runCommandQuiet("git", "push", "origin", branch); err != nil {
		fmt.Printf(" %s⚠%s %s(could not push, continuing)%s\n", Yellow, Reset, Dim, Reset)
	} else {
		fmt.Printf(" %s✓%s\n", Green, Reset)
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
	fmt.Printf(" %s✓%s\n", Green, Reset)

	return nil
}

func resyncMode(deployBranch string, branches []BranchInfo) {
	fmt.Printf("\n%s%s Resync Mode %s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s%s\n", Dim, strings.Repeat("─", 50), Reset)
	fmt.Printf("Deploy branch: %s%s%s\n", Yellow, deployBranch, Reset)

	// Get previously merged branches
	mergedBranches := getMergedBranchesFromLog()

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

			showCompletionSummary(deployBranch, mergedBranches)
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

			showCompletionSummary(deployBranch, branchNames)
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

	showCompletionSummary(deployBranch, branchNames)
	offerPreviewDeploy(deployBranch)
}

func showCompletionSummary(deployBranch string, branches []string) {
	fmt.Printf("\n%s%s%s\n", Dim, strings.Repeat("─", 50), Reset)
	fmt.Printf("%s✓ Deploy branch '%s' updated!%s\n\n", Green, deployBranch, Reset)

	fmt.Printf("%sBranches synced:%s\n", Bold, Reset)
	for _, b := range branches {
		fmt.Printf("  • %s\n", b)
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

	fmt.Printf("\n%sDeploy to preview server?%s\n", Bold, Reset)
	server := selectPreviewServer()

	if server != "skip" {
		fmt.Printf("\nDeploying to %s%s%s preview server...\n", Cyan, server, Reset)
		fmt.Printf("%sThis will take 4-8 minutes. Output shown below:%s\n", Dim, Reset)
		fmt.Printf("%s%s%s\n", Dim, strings.Repeat("─", 50), Reset)

		var deployCmd string
		if server == "history" {
			deployCmd = fmt.Sprintf("checkout-history %s", deployBranch)
		} else {
			deployCmd = fmt.Sprintf("checkout-feedback %s", deployBranch)
		}

		// For deploy, we do want to see output since it takes a while
		if err := runCommand("zsh", "-i", "-c", deployCmd); err != nil {
			fmt.Printf("%sWarning: Deploy command may have failed: %v%s\n", Yellow, err, Reset)
		}
		fmt.Printf("%s%s%s\n", Dim, strings.Repeat("─", 50), Reset)
	} else {
		fmt.Printf("\n%sTo deploy later, run:%s\n", Dim, Reset)
		fmt.Printf("  checkout-history %s\n", deployBranch)
		fmt.Printf("  # or\n")
		fmt.Printf("  checkout-feedback %s\n", deployBranch)
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
	fmt.Printf("Format: %s<description> (e.g., %swhiparound-integration)\n", datePrefix, datePrefix)
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

	for i, branch := range selectedBranches {
		fmt.Printf("\n%s[%d/%d]%s ", Cyan, i+1, len(selectedBranches), Reset)
		if err := syncAndMergeBranch(branch.Name, branchName); err != nil {
			os.Exit(1)
		}
		mergedBranches[branch.Author] = append(mergedBranches[branch.Author], branch.Name)
	}

	// Step 7: Summary
	fmt.Printf("\n%s%s%s\n", Dim, strings.Repeat("─", 50), Reset)
	fmt.Printf("%s✓ Deploy branch '%s' is ready!%s\n\n", Green, branchName, Reset)

	fmt.Printf("%sBranches included:%s\n", Bold, Reset)
	var authors []string
	for author := range mergedBranches {
		authors = append(authors, author)
	}
	sort.Strings(authors)

	for _, author := range authors {
		fmt.Printf("\n%s%s%s\n", Yellow, author, Reset)
		for _, b := range mergedBranches[author] {
			fmt.Printf("- %s\n", b)
		}
	}

	// Step 8: Deploy to preview
	fmt.Printf("\n%sStep 7: Deploy to preview server?%s\n", Bold, Reset)
	offerPreviewDeploy(branchName)
}

func main() {
	fmt.Printf("\n%s%s Deploy Branch Builder %s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s%s\n", Dim, strings.Repeat("─", 50), Reset)

	// Check we're in the osg repo
	output, err := runCommandSilent("git", "rev-parse", "--show-toplevel")
	if err != nil || !strings.Contains(output, "osg") {
		fmt.Printf("%sError: Please run this from the osg repository%s\n", Red, Reset)
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
