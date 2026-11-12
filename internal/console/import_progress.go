package console

import (
	"context"
	"database/sql"
	"errors"
	"os"
)

const importProgressBatchSize = 64

type importProgress struct {
	console *Console
	db      *sql.DB
	pending []importItemUpdate
	failed  bool
}

func (c *Console) beginImportProgress(task videoImportTask) (*importProgress, error) {
	if _, err := os.Lstat(c.files().VideoImport); os.IsNotExist(err) {
		// Legacy JSON is migrated only on an explicit resume/cleanup operation.
		if err := c.saveImportTask(task); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, importSaveError(err)
	}
	db, err := openImportStore(c.files().VideoImport, true)
	if err != nil {
		return nil, importSaveError(err)
	}
	if err := removeLegacyImport(c.files().State("video-import")); err != nil {
		db.Close()
		return nil, importSaveError(err)
	}
	return &importProgress{console: c, db: db}, nil
}

func (p *importProgress) record(index int, item videoImportItem, immediate bool) error {
	p.pending = append(p.pending, importItemUpdate{index: index, item: item})
	if immediate || len(p.pending) == importProgressBatchSize {
		return p.flush()
	}
	return nil
}

func (p *importProgress) flush() error {
	if len(p.pending) == 0 || p.failed {
		return nil
	}
	// Deliberately independent of the worker's cancellation context: pause must
	// preserve completed work, especially a publication that just succeeded.
	if err := saveImportItems(p.db, p.pending); err != nil {
		p.failed = true
		return importSaveError(err)
	}
	p.console.publishImportItems(p.pending)
	clear(p.pending)
	p.pending = p.pending[:0]
	return nil
}

func (p *importProgress) finish(result *error) {
	if err := p.flush(); err != nil {
		// A persistence failure must remain visible even when pause triggered it.
		*result = err
	}
	if err := p.db.Close(); err != nil {
		if errors.Is(*result, context.Canceled) {
			*result = nil
		}
		*result = errors.Join(*result, importSaveError(err))
	}
}
