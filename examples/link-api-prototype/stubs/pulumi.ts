export declare class Output<T> {
  private __t: T;
  apply<U>(fn: (t: T) => U): Output<U>;
}

export type Input<T> = T | Output<T>;

export declare function interpolate(
  strings: TemplateStringsArray,
  ...values: Input<string | number>[]
): Output<string>;

export declare function secret(value: Input<string>): Output<string>;
