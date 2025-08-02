/*
Copyright © 2023 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"
	"os"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// storageCmd represents the storage command
var storageCmd = &cobra.Command{
	Use:   "storage",
	Short: "Manage storage",
}

var disableStorageCmd = &cobra.Command{
	Use:   "disable [mount path]",
	Short: "Disable a storage by mount path",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("mount path is required")
		}
		mountPath := args[0]
		Init()
		defer Release()
		storage, err := db.GetStorageByMountPath(mountPath)
		if err != nil {
			return fmt.Errorf("failed to query storage: %+v", err)
		}
		storage.Disabled = true
		err = db.UpdateStorage(storage)
		if err != nil {
			return fmt.Errorf("failed to update storage: %+v", err)
		}
		utils.Log.Infof("Storage with mount path [%s] has been disabled from CLI", mountPath)
		fmt.Printf("Storage with mount path [%s] has been disabled\n", mountPath)
		return nil
	},
}

var deleteStorageCmd = &cobra.Command{
	Use:   "delete [id]",
	Short: "Delete a storage by id",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("id is required")
		}
		id, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("id must be a number")
		}

		if force, _ := cmd.Flags().GetBool("force"); force {
			fmt.Printf("Are you sure you want to delete storage with id [%d]? [y/N]: ", id)
			var confirm string
			fmt.Scanln(&confirm)
			if confirm != "y" && confirm != "Y" {
				fmt.Println("Delete operation cancelled.")
				return nil
			}
		}

		Init()
		defer Release()
		err = db.DeleteStorageById(uint(id))
		if err != nil {
			return fmt.Errorf("failed to delete storage by id: %+v", err)
		}
		utils.Log.Infof("Storage with id [%d] have been deleted from CLI", id)
		fmt.Printf("Storage with id [%d] have been deleted\n", id)
		return nil
	},
}

var baseStyle = lipgloss.NewStyle().
	BorderStyle(lipgloss.NormalBorder()).
	BorderForeground(lipgloss.Color("240"))

type model struct {
	table table.Model
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			if m.table.Focused() {
				m.table.Blur()
			} else {
				m.table.Focus()
			}
		case "q", "ctrl+c":
			return m, tea.Quit
			//case "enter":
			//	return m, tea.Batch(
			//		tea.Printf("Let's go to %s!", m.table.SelectedRow()[1]),
			//	)
		}
	}
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m model) View() string {
	return baseStyle.Render(m.table.View()) + "\n"
}

var storageTableHeight int
var listStorageCmd = &cobra.Command{
	Use:   "list",
	Short: "List all storages",
	RunE: func(cmd *cobra.Command, args []string) error {
		Init()
		defer Release()
		storages, _, err := db.GetStorages(1, -1)
		if err != nil {
			return fmt.Errorf("failed to query storages: %+v", err)
		} else {
			fmt.Printf("Found %d storages\n", len(storages))
			columns := []table.Column{
				{Title: "ID", Width: 4},
				{Title: "Driver", Width: 16},
				{Title: "Mount Path", Width: 30},
				{Title: "Enabled", Width: 7},
			}

			var rows []table.Row
			for i := range storages {
				storage := storages[i]
				enabled := "true"
				if storage.Disabled {
					enabled = "false"
				}
				rows = append(rows, table.Row{
					strconv.Itoa(int(storage.ID)),
					storage.Driver,
					storage.MountPath,
					enabled,
				})
			}
			t := table.New(
				table.WithColumns(columns),
				table.WithRows(rows),
				table.WithFocused(true),
				table.WithHeight(storageTableHeight),
			)

			s := table.DefaultStyles()
			s.Header = s.Header.
				BorderStyle(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Color("240")).
				BorderBottom(true).
				Bold(false)
			s.Selected = s.Selected.
				Foreground(lipgloss.Color("229")).
				Background(lipgloss.Color("57")).
				Bold(false)
			t.SetStyles(s)

			m := model{t}
			if _, err := tea.NewProgram(m).Run(); err != nil {
				fmt.Printf("failed to run program: %+v\n", err)
				os.Exit(1)
			}
		}
		return nil
	},
}

func init() {

	RootCmd.AddCommand(storageCmd)
	storageCmd.AddCommand(disableStorageCmd)
	storageCmd.AddCommand(listStorageCmd)
	storageCmd.PersistentFlags().IntVarP(&storageTableHeight, "height", "H", 10, "Table height")
	storageCmd.AddCommand(deleteStorageCmd)
	deleteStorageCmd.Flags().BoolP("force", "f", false, "Force delete without confirmation")
	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// storageCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// storageCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
