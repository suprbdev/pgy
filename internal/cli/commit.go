package cli

import (
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "os"
    "path/filepath"
    "regexp"
    "sort"
    "strconv"
    "strings"
    "time"

    "github.com/spf13/cobra"

    "github.com/suprbdev/pgy/internal/group"
)

func cmdCommit() *cobra.Command {
    var allowEmpty bool
    c := &cobra.Command{
        Use:   "commit [name]",
        Short: "Commit current buffer to a numbered migration file (one per group for split diffs)",
        Args:  cobra.MaximumNArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            cfg := FromContext(cmd.Context())
            name := ""
            if len(args) == 1 { name = args[0] }
            b, err := os.ReadFile(cfg.BufferPath)
            if err != nil {
                if os.IsNotExist(err) {
                    return fmt.Errorf("buffer not found: %s", cfg.BufferPath)
                }
                return err
            }
            parts := []group.Section{{SQL: string(b)}}
            if sections := group.Parse(string(b)); sections != nil {
                parts = parts[:0]
                for _, s := range sections {
                    if s.SQL != "" { parts = append(parts, s) }
                }
                if len(parts) == 0 { parts = []group.Section{{}} }
            }
            if len(parts) == 1 && len(parts[0].SQL) == 0 && !allowEmpty {
                return fmt.Errorf("buffer is empty; use --allow-empty to force")
            }
            if err := os.MkdirAll(cfg.MigrationsDir, 0o755); err != nil { return err }
            next, err := nextMigrationNumber(cfg.MigrationsDir)
            if err != nil { return err }
            created := time.Now().UTC()
            written := make([]string, 0, len(parts))
            for i, part := range parts {
                filename, err := writeMigration(cfg.MigrationsDir, next+i, slugify(name+" "+part.Name), []byte(part.SQL), created)
                if err != nil {
                    for _, f := range written { _ = os.Remove(f) }
                    return err
                }
                written = append(written, filename)
            }
            _ = os.Remove(cfg.BufferPath)
            if !cfg.Quiet {
                for _, f := range written { fmt.Printf("created %s\n", f) }
            }
            return nil
        },
    }
    c.Flags().BoolVar(&allowEmpty, "allow-empty", false, "Commit even if buffer is empty")
    return c
}

func writeMigration(dir string, num int, slug string, body []byte, created time.Time) (string, error) {
    base := fmt.Sprintf("%04d", num)
    if slug != "" { base = base + "_" + slug }
    filename := filepath.Join(dir, base+".sql")
    sum := sha256.Sum256(body)
    header := fmt.Sprintf("-- pgy %s\n-- created %s\n-- checksum %s\n\n", VersionString(), created.Format(time.RFC3339), hex.EncodeToString(sum[:]))
    if err := os.WriteFile(filename, append([]byte(header), body...), 0o644); err != nil { return "", err }
    return filename, nil
}

func nextMigrationNumber(dir string) (int, error) {
    entries, err := os.ReadDir(dir)
    if err != nil {
        if os.IsNotExist(err) { return 1, nil }
        return 0, err
    }
    nums := []int{}
    re := regexp.MustCompile(`^(\d{4})`)
    for _, e := range entries {
        if e.IsDir() { continue }
        m := re.FindStringSubmatch(e.Name())
        if len(m) == 2 {
            n, err := strconv.Atoi(m[1])
            if err != nil { continue }
            nums = append(nums, n)
        }
    }
    sort.Ints(nums)
    if len(nums) == 0 { return 1, nil }
    return nums[len(nums)-1] + 1, nil
}

func slugify(s string) string {
    s = strings.ToLower(strings.TrimSpace(s))
    s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "_")
    s = strings.Trim(s, "_")
    return s
}


