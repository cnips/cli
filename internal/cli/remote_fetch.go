package cli

import (
	"fmt"
	"sync"

	"github.com/cnips/cli/internal/platform"
)

func fetchWorkspaceLists(client *platform.Client, workspaceIDs []string) (
	[]platform.Transformation,
	[]platform.Source,
	[]platform.Destination,
	[]platform.Function,
	[]platform.GlobalVariable,
	[]platform.Configuration,
	[]platform.Pipeline,
	error,
) {
	var (
		txList       []platform.Transformation
		srcList      []platform.Source
		dstList      []platform.Destination
		fnList       []platform.Function
		gvList       []platform.GlobalVariable
		configList   []platform.Configuration
		pipelineList []platform.Pipeline
	)
	err := runConcurrent(
		func() error {
			var err error
			txList, err = collectByID(workspaceIDs, client.ListTransformations, func(t platform.Transformation) string { return t.ID })
			return wrapListErr("transformations", err)
		},
		func() error {
			var err error
			srcList, err = collectByID(workspaceIDs, client.ListSources, func(s platform.Source) string { return s.ID })
			return wrapListErr("sources", err)
		},
		func() error {
			var err error
			dstList, err = collectByID(workspaceIDs, client.ListDestinations, func(d platform.Destination) string { return d.ID })
			return wrapListErr("destinations", err)
		},
		func() error {
			var err error
			fnList, err = collectByID(workspaceIDs, client.ListFunctions, func(f platform.Function) string { return f.ID })
			return wrapListErr("functions", err)
		},
		func() error {
			var err error
			gvList, err = collectByID(workspaceIDs, client.ListGlobalVariables, func(gv platform.GlobalVariable) string { return gv.ID })
			return wrapListErr("global variables", err)
		},
		func() error {
			var err error
			configList, err = collectByID(workspaceIDs, client.ListConfigurations, func(c platform.Configuration) string { return c.ID })
			return wrapListErr("configurations", err)
		},
		func() error {
			var err error
			pipelineList, err = collectByID(workspaceIDs, client.ListPipelines, func(p platform.Pipeline) string { return p.ID })
			return wrapListErr("pipelines", err)
		},
	)
	return txList, srcList, dstList, fnList, gvList, configList, pipelineList, err
}

func printFetchedCounts(
	txList []platform.Transformation,
	srcList []platform.Source,
	dstList []platform.Destination,
	fnList []platform.Function,
	gvList []platform.GlobalVariable,
	configList []platform.Configuration,
	pipelineList []platform.Pipeline,
) {
	fmt.Printf("  fetching transformations... %d\n", len(txList))
	fmt.Printf("  fetching sources...         %d\n", len(srcList))
	fmt.Printf("  fetching destinations...    %d\n", len(dstList))
	fmt.Printf("  fetching functions...       %d\n", len(fnList))
	fmt.Printf("  fetching global variables... %d\n", len(gvList))
	fmt.Printf("  fetching configurations...  %d\n", len(configList))
	fmt.Printf("  fetching pipelines...       %d\n", len(pipelineList))
}

func runConcurrent(tasks ...func() error) error {
	errCh := make(chan error, len(tasks))
	var wg sync.WaitGroup
	for _, task := range tasks {
		task := task
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := task(); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func wrapListErr(label string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("list %s: %w", label, err)
}
