package gameplay

import (
	"go.uber.org/fx"

	"github.com/starvy/flagfish/internal/jobs"
)

// Module provides the gameplay service. The announcement enqueue rides the submit transaction, so the
// service takes an Inserter (not a standalone client) and binds it to the JobInserter seam here.
var Module = fx.Module(
	"gameplay",
	fx.Provide(
		func(i *jobs.Inserter) JobInserter { return i },
		New,
	),
)
