import { type ButtonHTMLAttributes, type ReactNode, type Ref } from 'react';

export type ButtonVariant = 'primary' | 'secondary' | 'onDark' | 'icon';

type BaseButtonProps = Omit<
  ButtonHTMLAttributes<HTMLButtonElement>,
  'children' | 'type'
> & {
  ref?: Ref<HTMLButtonElement>;
  type?: 'button' | 'submit' | 'reset';
  arrow?: boolean;
};

export type ButtonProps =
  | (BaseButtonProps & {
      variant: 'icon';
      'aria-label': string;
      children: ReactNode;
    })
  | (BaseButtonProps & {
      variant?: Exclude<ButtonVariant, 'icon'>;
      children: ReactNode;
    });

const VARIANT_CLASSES: Record<ButtonVariant, string> = {
  primary:
    'bg-green-600 text-paper hover:bg-dark hover:-translate-y-px border border-transparent',
  secondary:
    'bg-paper text-green-600 border-[1.5px] border-line hover:bg-mint-soft hover:border-green-light hover:-translate-y-px',
  onDark:
    'bg-green-light text-terminal hover:bg-mint hover:-translate-y-px border border-transparent',
  icon: 'bg-paper text-ink-2 border-[1.5px] border-line hover:bg-mint-soft hover:border-green-light hover:text-green-600 w-11 px-0',
};

const TRAILING_ARROW = '→';

/**
 * Accessible button primitive enforcing 44 px minimum touch targets,
 * visible focus outline, and non-red disabled styling.
 */
export function Button({
  ref,
  variant = 'primary',
  type = 'button',
  arrow = false,
  className = '',
  disabled = false,
  children,
  ...rest
}: ButtonProps) {
  const paddingClass = variant === 'icon' ? 'px-0' : 'px-5';

  return (
    <button
      ref={ref}
      type={type}
      disabled={disabled}
      className={`group inline-flex min-h-[44px] items-center justify-center gap-2 rounded-sm font-bold text-[15px] transition duration-150 ease-out focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green disabled:cursor-not-allowed disabled:border-transparent disabled:bg-on-dark-muted disabled:text-paper disabled:hover:translate-y-0 ${paddingClass} ${VARIANT_CLASSES[variant]} ${className}`.trim()}
      {...rest}
    >
      {children}
      {arrow && variant !== 'icon' ? (
        <span
          aria-hidden="true"
          className="inline-block transition-transform duration-150 group-hover:translate-x-[3px] group-disabled:translate-x-0"
        >
          {TRAILING_ARROW}
        </span>
      ) : null}
    </button>
  );
}
