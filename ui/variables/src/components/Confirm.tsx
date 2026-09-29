import { names, plural } from "../model";
import { useValue } from "../signals";
import { cancelRemoval, confirmRemoval, removing, saving } from "../store";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "./ui/alert-dialog";

export function Confirm() {
  const asked = useValue(removing);
  const busy = useValue(saving);
  return (
    <AlertDialog open={asked !== null} onOpenChange={(open) => !open && cancelRemoval()}>
      {asked && (
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove {plural(asked.cells.length, "value")}?</AlertDialogTitle>
            <AlertDialogDescription>
              The stored value goes away for {names(asked.cells.map((cell) => cell.at.key))}.
              History keeps the versions; nothing else on this page is touched.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel size="sm" disabled={busy} data-action="cancel-remove">
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              size="sm"
              disabled={busy}
              data-action="confirm-remove"
              onClick={() => void confirmRemoval()}
            >
              {busy ? "Removing…" : "Remove"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      )}
    </AlertDialog>
  );
}
