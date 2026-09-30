package manifest

import "fmt"

type InvalidDeclarationError struct {
	Source string
	Reason string
}

func (e *InvalidDeclarationError) Error() string {
	return sourceOrUnknown(e.Source) + ": " + e.Reason
}

type ServedTwiceError struct {
	Subject      string
	FirstSource  string
	FirstWorker  string
	SecondSource string
	SecondWorker string
}

func (e *ServedTwiceError) Error() string {
	return fmt.Sprintf(
		"%s is declared at %s served by worker %q and at %s served by worker %q: it runs on one worker, so declare it once",
		e.Subject, sourceOrUnknown(e.FirstSource), e.FirstWorker, sourceOrUnknown(e.SecondSource), e.SecondWorker,
	)
}

func refuse(d declaredResource, format string, args ...any) error {
	return &InvalidDeclarationError{Source: d.Source, Reason: d.label() + " " + fmt.Sprintf(format, args...)}
}

func firstRefusal(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
