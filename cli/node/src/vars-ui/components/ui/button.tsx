import { Button as ButtonPrimitive } from "@base-ui/react/button"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "@/lib/utils"

const buttonVariants = cva(
  "group/button inline-flex shrink-0 items-center justify-center gap-1.5 rounded-none whitespace-nowrap transition-colors outline-none select-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-45 aria-invalid:border-destructive",
  {
    variants: {
      variant: {
        default:
          "border border-primary bg-primary font-sans font-semibold text-primary-foreground hover:border-foreground hover:bg-foreground hover:text-background",
        outline:
          "border-[1.5px] border-foreground bg-transparent font-sans font-semibold text-foreground hover:bg-foreground hover:text-background aria-expanded:bg-foreground aria-expanded:text-background",
        command:
          "border-[1.5px] border-foreground bg-transparent font-mono text-foreground hover:text-primary aria-expanded:text-primary",
        ghost:
          "border border-border bg-transparent font-mono text-muted-foreground hover:border-primary hover:text-primary aria-expanded:border-primary aria-expanded:text-primary aria-pressed:text-foreground",
        destructive:
          "border-[1.5px] border-destructive bg-transparent font-sans font-semibold text-destructive hover:bg-destructive hover:text-destructive-foreground focus-visible:ring-destructive",
        link: "border-0 bg-transparent font-sans text-foreground hover:text-primary",
      },
      size: {
        default: "h-11 px-6 text-[15px]",
        sm: "h-9 px-4 text-sm",
        xs: "h-8 px-3 text-[13px]",
        icon: "size-11 text-base",
        "icon-sm": "size-9 text-sm",
        "icon-xs": "size-7 text-[13px]",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
)

function Button({
  className,
  variant = "default",
  size = "default",
  ...props
}: ButtonPrimitive.Props & VariantProps<typeof buttonVariants>) {
  return (
    <ButtonPrimitive
      data-slot="button"
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    />
  )
}

export { Button, buttonVariants }
