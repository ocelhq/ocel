package commands

import (
	"strconv"

	"github.com/spf13/cobra"
)

const mutatesAnnotation = "ocel.mutates"

func DeclareMutating(cmd *cobra.Command) *cobra.Command {
	return declareMutation(cmd, true)
}

func DeclareReadOnly(cmd *cobra.Command) *cobra.Command {
	return declareMutation(cmd, false)
}

func declareMutation(cmd *cobra.Command, mutates bool) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[mutatesAnnotation] = strconv.FormatBool(mutates)
	return cmd
}

func FindMutation(cmd *cobra.Command) (mutates, declared bool) {
	value, declared := cmd.Annotations[mutatesAnnotation]
	if !declared {
		return false, false
	}
	mutates, err := strconv.ParseBool(value)
	return mutates, err == nil
}
