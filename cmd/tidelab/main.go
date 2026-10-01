package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gofiber/fiber/v3"
	executionservices "github.com/stupidprogrammer4/tidelab/internal/modules/execution/services"
	marketinfra "github.com/stupidprogrammer4/tidelab/internal/modules/market/infra"
	marketservices "github.com/stupidprogrammer4/tidelab/internal/modules/market/services"
	"github.com/stupidprogrammer4/tidelab/internal/modules/system/infra"
	"github.com/stupidprogrammer4/tidelab/internal/modules/system/routers"
	"github.com/stupidprogrammer4/tidelab/internal/modules/system/services"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "tidelab:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: tidelab version | config check [-file PATH] | book inspect [-fixture PATH] | estimate [options] | serve [-config PATH]")
	}
	service := services.InfoService{}
	switch args[0] {
	case "version":
		if len(args) != 1 {
			return errors.New("version takes no arguments")
		}
		version := service.Version()
		_, err := fmt.Fprintf(out, "%s %s\n", version.Name, version.Version)
		return err
	case "config":
		if len(args) < 2 || args[1] != "check" {
			return errors.New("usage: tidelab config check [-file PATH]")
		}
		flags := flag.NewFlagSet("config check", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		path := flags.String("file", "", "JSON config path")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			return errors.New("usage: tidelab config check [-file PATH]")
		}
		config, err := infra.LoadConfig(*path)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(config)
	case "serve":
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		path := flags.String("config", "", "JSON config path")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return errors.New("usage: tidelab serve [-config PATH]")
		}
		config, err := infra.LoadConfig(*path)
		if err != nil {
			return err
		}
		app := fiber.New(fiber.Config{ReadTimeout: 5 * time.Second})
		routers.Register(app, service)
		fmt.Fprintf(out, "TideLab listening on %s\n", config.ListenAddr)
		return app.Listen(config.ListenAddr)
	case "book":
		if len(args) < 2 || args[1] != "inspect" {
			return errors.New("usage: tidelab book inspect [-fixture PATH]")
		}
		flags := flag.NewFlagSet("book inspect", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		path := flags.String("fixture", "testdata/synthetic-market.json", "synthetic book fixture")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			return errors.New("usage: tidelab book inspect [-fixture PATH]")
		}
		market := marketservices.OfflineBookService{Loader: marketinfra.FileFixtureLoader{}}
		report, err := market.Inspect(*path)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(report)
	case "estimate":
		flags := flag.NewFlagSet("estimate", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		fixture := flags.String("fixture", "testdata/execution-book.json", "synthetic book fixture")
		side := flags.String("side", "", "buy or sell")
		baseQuantity := flags.String("base-qty", "", "base quantity decimal")
		quoteBudget := flags.String("quote-budget", "", "fee-inclusive buy budget decimal")
		feeBps := flags.String("fee-bps", "", "explicit fee basis points")
		maxSlippage := flags.String("max-slippage-bps", "", "optional slippage limit")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return errors.New("usage: tidelab estimate --side buy|sell (--base-qty DECIMAL | --quote-budget DECIMAL) --fee-bps DECIMAL [--max-slippage-bps DECIMAL] [--fixture PATH]")
		}
		market := marketservices.OfflineBookService{Loader: marketinfra.FileFixtureLoader{}}
		estimator := executionservices.EstimateOffline{Source: market}
		report, err := estimator.Run(*fixture, executionservices.Input{
			Side: *side, BaseQuantity: *baseQuantity, QuoteBudget: *quoteBudget,
			FeeBps: *feeBps, MaxSlippageBps: *maxSlippage,
		})
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(report)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
