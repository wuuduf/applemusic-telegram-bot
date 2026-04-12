package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/wuuduf/applemusic-telegram-bot/utils/runv3"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "mp4decrypt fallback: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	inPath, outPath, keySpec, err := parseArgs(args)
	if err != nil {
		return err
	}
	key, err := parseKey(keySpec)
	if err != nil {
		return err
	}

	inFile, err := os.Open(inPath)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer inFile.Close()

	outFile, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer func() {
		_ = outFile.Close()
	}()

	if err := runv3.DecryptMP4(inFile, key, outFile); err != nil {
		return err
	}
	return outFile.Close()
}

func parseArgs(args []string) (input, output, key string, err error) {
	positionals := make([]string, 0, 2)
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "" || arg == "--":
			continue
		case arg == "--help" || arg == "-h":
			return "", "", "", errors.New("usage: mp4decrypt --key <KID:KEY|TRACK:KEY|KEY> <input.mp4> <output.mp4>")
		case arg == "--key" || arg == "-k":
			if i+1 >= len(args) {
				return "", "", "", errors.New("missing value for --key")
			}
			key = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(arg, "--key="):
			key = strings.TrimSpace(strings.TrimPrefix(arg, "--key="))
		default:
			if strings.HasPrefix(arg, "-") {
				return "", "", "", fmt.Errorf("unsupported option %q", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if key == "" {
		return "", "", "", errors.New("missing --key")
	}
	if len(positionals) != 2 {
		return "", "", "", errors.New("usage: mp4decrypt --key <KID:KEY|TRACK:KEY|KEY> <input.mp4> <output.mp4>")
	}
	return positionals[0], positionals[1], key, nil
}

func parseKey(spec string) ([]byte, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, errors.New("empty key")
	}
	parts := strings.Split(spec, ":")
	hexKey := strings.TrimSpace(parts[len(parts)-1])
	hexKey = strings.TrimPrefix(hexKey, "0x")
	hexKey = strings.TrimPrefix(hexKey, "0X")
	hexKey = strings.ReplaceAll(hexKey, "-", "")
	if hexKey == "" {
		return nil, fmt.Errorf("invalid key format: %q", spec)
	}
	if len(hexKey)%2 == 1 {
		hexKey = "0" + hexKey
	}
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	if len(key) == 0 {
		return nil, errors.New("decoded key is empty")
	}
	return key, nil
}
