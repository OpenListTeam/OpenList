package cmd

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/bootstrap"
	"github.com/spf13/cobra"
)

// ServerCmd represents the server command
var ServerCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the server at the specified address",
	Long: `Start the server at the specified address
the address is defined in config file`,
	Run: func(cmd *cobra.Command, args []string) {
		bootstrap.Init()
		defer bootstrap.Release()
		bootstrap.Start()
		// Wait for interrupt signal to gracefully shutdown the server with
		// a timeout of 1 second.
		quit := make(chan os.Signal, 1)
		// kill (no param) default send syscanll.SIGTERM
		// kill -2 is syscall.SIGINT
		// kill -9 is syscall. SIGKILL but can"t be catch, so don't need add it
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit
		bootstrap.Shutdown(1 * time.Second)
	},
}

func init() {
	RootCmd.AddCommand(ServerCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// serverCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// serverCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}

// OutOpenListInit 暴露用于外部启动server的函数
func OutOpenListInit() {
	var (
		cmd  *cobra.Command
		args []string
	)
	ServerCmd.Run(cmd, args)
}
