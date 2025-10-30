package cli

import (
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "os"
    "path/filepath"
    "regexp"
    "sort"
    "strings"
    "time"

    "github.com/spf13/cobra"
)

func cmdCommit() *cobra.Command {
    var allowEmpty bool
    c := &cobra.Command{
        Use:   "commit [name]",
        Short: "Commit current buffer to a numbered migration file",
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
            if len(b) == 0 && !allowEmpty {
                return fmt.Errorf("buffer is empty; use --allow-empty to force")
            }
            if err := os.MkdirAll(cfg.MigrationsDir, 0o755); err != nil { return err }
            next, err := nextMigrationNumber(cfg.MigrationsDir)
            if err != nil { return err }
            slug := slugify(name)
            base := fmt.Sprintf("%04d", next)
            if slug != "" { base = base + "_" + slug }
            filename := filepath.Join(cfg.MigrationsDir, base+".sql")
            sum := sha256.Sum256(b)
            header := fmt.Sprintf("-- pgy %s\n-- created %s\n-- checksum %s\n\n", VersionString(), time.Now().UTC().Format(time.RFC3339), hex.EncodeToString(sum[:]))
            if err := os.WriteFile(filename, append([]byte(header), b...), 0o644); err != nil { return err }
            _ = os.Remove(cfg.BufferPath)
            if !cfg.Quiet { fmt.Printf("created %s\n", filename) }
            return nil
        },
    }
    c.Flags().BoolVar(&allowEmpty, "allow-empty", false, "Commit even if buffer is empty")
    return c
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
            var n int
            fmt.Sscanf(m[1], "%d", &n)
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


