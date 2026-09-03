import { mergeProps } from "@base-ui/react/merge-props"
import { useRender } from "@base-ui/react/use-render"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "@/lib/utils"

const badgeVariants = cva(
  "group/badge inline-flex w-fit shrink-0 items-center justify-center gap-1 rounded-none border px-2 py-px font-mono text-[12.5px] whitespace-nowrap transition-colors focus-visible:ring-2 focus-visible:ring-ring",
  {
    variants: {
      variant: {
        default: "border-foreground bg-card text-foreground",
        secondary: "border-border bg-card text-muted-foreground",
        destructive: "border-destructive bg-card text-destructive",
        outline: "border-primary bg-transparent text-primary",
        ghost: "border-dashed border-muted-foreground bg-transparent text-muted-foreground",
        link: "border-0 bg-transparent text-foreground underline-offset-4 hover:text-primary hover:underline",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
)

function Badge({
  className,
  variant = "default",
  render,
  ...props
}: useRender.ComponentProps<"span"> & VariantProps<typeof badgeVariants>) {
  return useRender({
    defaultTagName: "span",
    props: mergeProps<"span">(
      {
        className: cn(badgeVariants({ variant }), className),
      },
      props
    ),
    render,
    state: {
      slot: "badge",
      variant,
    },
  })
}

export { Badge, badgeVariants }
