interface TrustedTypePolicy {
  createHTML(input: string): unknown;
}

interface TrustedTypePolicyFactory {
  createPolicy(name: string, rules: { createHTML(input: string): string }): TrustedTypePolicy;
}

const reference = /^&[A-Za-z][A-Za-z\d]{0,31};$/;

function onlyReference(input: string): string {
  if (!reference.test(input)) throw new TypeError('atrium-md accepts one named character reference');
  return input;
}

const factory: TrustedTypePolicyFactory | undefined = Reflect.get(globalThis, 'trustedTypes');
const policy = factory?.createPolicy('atrium-md', { createHTML: onlyReference });
const element = document.createElement('i');

export function decodeNamedCharacterReference(value: string): string | false {
  const input = `&${value};`;
  if (!reference.test(input)) return false;
  Reflect.set(element, 'innerHTML', policy ? policy.createHTML(input) : input);
  const character = element.textContent ?? '';
  if (character.charCodeAt(character.length - 1) === 59 && value !== 'semi') return false;
  return character === input ? false : character;
}
