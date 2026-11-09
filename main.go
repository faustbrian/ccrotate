// ccrotate switches the Claude Code login to the next saved account when the
// active account reaches a usage threshold.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
)

const usageText = `usage:
  ccrotate add <name>            save the account currently logged in to Claude Code
  ccrotate rm <name>             forget a saved account
  ccrotate on [--5h 98] [--7d 98]  rotate at these usage percentages
  ccrotate off                   stop rotating`

const defaultThresholdPercent = 98.0

func main() {
	log.SetFlags(log.LstdFlags)

	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ccrotate:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New(usageText)
	}

	lock, err := acquireStateLock()
	if err != nil {
		return err
	}
	defer lock.Close()

	command, commandArgs := args[0], args[1:]
	switch command {
	case "add":
		name, err := singleName(commandArgs)
		if err != nil {
			return err
		}
		return addAccount(name)
	case "rm":
		name, err := singleName(commandArgs)
		if err != nil {
			return err
		}
		return removeAccount(name)
	case "on":
		return turnOn(commandArgs)
	case "off":
		return turnOff()
	case "tick":
		return tick()
	default:
		return errors.New(usageText)
	}
}

func singleName(args []string) (string, error) {
	if len(args) != 1 {
		return "", errors.New(usageText)
	}

	return args[0], nil
}

func turnOn(args []string) error {
	flags := flag.NewFlagSet("on", flag.ContinueOnError)
	fiveHour := flags.Float64("5h", defaultThresholdPercent, "5-hour usage percentage that triggers rotation")
	sevenDay := flags.Float64("7d", defaultThresholdPercent, "7-day usage percentage that triggers rotation")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if flags.NArg() != 0 {
		return errors.New(usageText)
	}

	for _, threshold := range []float64{*fiveHour, *sevenDay} {
		if threshold <= 0 || threshold > 100 {
			return fmt.Errorf("threshold %v must be above 0 and at most 100", threshold)
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if len(cfg.Accounts) < 2 {
		return fmt.Errorf("rotation needs at least two saved accounts, have %d", len(cfg.Accounts))
	}

	cfg.FiveHourThreshold = *fiveHour
	cfg.SevenDayThreshold = *sevenDay
	if err := saveConfig(cfg); err != nil {
		return err
	}

	if err := installAgent(); err != nil {
		return err
	}

	fmt.Printf("rotation on: 5h at %v%%, 7d at %v%%\n", *fiveHour, *sevenDay)
	return nil
}

func turnOff() error {
	if err := uninstallAgent(); err != nil {
		return err
	}

	fmt.Println("rotation off")
	return nil
}
