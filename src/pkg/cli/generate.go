package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/defang-io/defang/src/pkg/cli/client"
	"github.com/defang-io/defang/src/pkg/term"
	defangv1 "github.com/defang-io/defang/src/protos/io/defang/v1"
)

func Generate(ctx context.Context, client client.Client, language string, description string) ([]string, error) {
	if DoDryRun {
		term.Warn(" ! Dry run, not generating files")
		return nil, ErrDryRun
	}

	response, err := client.GenerateFiles(ctx, &defangv1.GenerateFilesRequest{
		AgreeTos: true, // agreement was already checked by the caller
		Language: language,
		Prompt:   description,
	})
	if err != nil {
		return nil, err
	}

	if term.DoDebug {
		// Print the files that were generated
		for _, file := range response.Files {
			term.Debug(file.Name + "\n```")
			term.Debug(file.Content)
			term.Debug("```")
			term.Debug("")
			term.Debug("")
		}
	}

	// Write each file to disk
	term.Info(" * Writing files to disk...")
	for _, file := range response.Files {
		// Print the files that were generated
		fmt.Println("   -", file.Name)
		// TODO: this will overwrite existing files
		if err = os.WriteFile(file.Name, []byte(file.Content), 0644); err != nil {
			return nil, err
		}
	}

	// put the file names in an array
	var fileNames []string
	for _, file := range response.Files {
		fileNames = append(fileNames, file.Name)
	}

	return fileNames, nil
}
