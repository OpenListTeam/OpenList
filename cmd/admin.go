/*
Copyright © 2022 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
	"github.com/spf13/cobra"
)

// AdminCmd represents the password command
var AdminCmd = &cobra.Command{
	Use:     "admin",
	Aliases: []string{"password"},
	Short:   "Show admin user's info and some operations about admin user's password",
	Run: func(cmd *cobra.Command, args []string) {
		Init()
		defer Release()
		admin, err := op.GetAdmin()
		if err != nil {
			utils.Log.Errorf("failed get admin user: %+v", err)
		} else {
			utils.Log.Infof("get admin user from CLI")
			fmt.Println("Admin user's username:", admin.Username)
			fmt.Println("The password can only be output at the first startup, and then stored as a hash value, which cannot be reversed")
			fmt.Println("You can reset the password with a random string by running [openlist admin random]")
			fmt.Println("You can also set a new password by running [openlist admin set NEW_PASSWORD]")
		}
	},
}

var RandomPasswordCmd = &cobra.Command{
	Use:   "random",
	Short: "Reset admin user's password to a random string",
	Run: func(cmd *cobra.Command, args []string) {
		utils.Log.Infof("reset admin user's password to a random string from CLI")
		newPwd := random.String(8)
		setAdminPassword(newPwd)
	},
}

var SetPasswordCmd = &cobra.Command{
	Use:   "set",
	Short: "Set admin user's password",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("Please enter the new password")
		}
		setAdminPassword(args[0])
		return nil
	},
}

var ShowTokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Show admin token",
	Run: func(cmd *cobra.Command, args []string) {
		Init()
		defer Release()
		token := setting.GetStr(conf.Token)
		utils.Log.Infof("show admin token from CLI")
		fmt.Println("Admin token:", token)
	},
}

func setAdminPassword(pwd string) {
	Init()
	defer Release()
	admin, err := op.GetAdmin()
	if err != nil {
		utils.Log.Errorf("failed get admin user: %+v", err)
		return
	}
	admin.SetPassword(pwd)
	if err := op.UpdateUser(admin); err != nil {
		utils.Log.Errorf("failed update admin user: %+v", err)
		return
	}
	utils.Log.Infof("admin user has been update from CLI")
	fmt.Println("admin user has been updated:")
	fmt.Println("username:", admin.Username)
	fmt.Println("password:", pwd)
	DelAdminCacheOnline()
}

func init() {
	RootCmd.AddCommand(AdminCmd)
	AdminCmd.AddCommand(RandomPasswordCmd)
	AdminCmd.AddCommand(SetPasswordCmd)
	AdminCmd.AddCommand(ShowTokenCmd)
	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// passwordCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// passwordCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
