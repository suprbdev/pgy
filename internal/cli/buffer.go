package cli

import (
    "fmt"
    "os"
    "strings"

    "github.com/spf13/cobra"
)

func cmdBuffer() *cobra.Command {
    var stat bool
    var clear bool
    c := &cobra.Command{
        Use:   "buffer",
        Short: "Show or manage the current SQL buffer",
        RunE: func(cmd *cobra.Command, args []string) error {
            cfg := FromContext(cmd.Context())
            if clear {
                if err := os.Remove(cfg.BufferPath); err != nil {
                    if !os.IsNotExist(err) { return err }
                }
                if !cfg.Quiet { fmt.Println("buffer cleared") }
                return nil
            }
            b, err := os.ReadFile(cfg.BufferPath)
            if err != nil {
                if os.IsNotExist(err) {
                    fmt.Printf("buffer %s not found\n", cfg.BufferPath)
                    return nil
                }
                return err
            }
            if stat {
                content := string(b)
                n := 0
                for _, s := range strings.Split(content, ";") {
                    if strings.TrimSpace(s) != "" { n++ }
                }
                fmt.Printf("%s: %d bytes, %d statements\n", cfg.BufferPath, len(b), n)
                return nil
            }
            os.Stdout.Write(b)
            return nil
        },
    }
    c.Flags().BoolVar(&stat, "stat", false, "Show summary only")
    c.Flags().BoolVar(&clear, "clear", false, "Delete buffer file")
    return c
}


