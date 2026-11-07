import {SpinButton, Text, type SpinButtonProps} from '@fluentui/react-components';
import {useRef} from 'react';
import {flushSync} from 'react-dom';

type NumberInputProps = Pick<SpinButtonProps, 'name' | 'id' | 'min' | 'max' | 'required' | 'disabled' | 'placeholder' | 'aria-label' | 'aria-describedby'> & {
  value: string;
  onChange: (value: string) => void;
  unit?: string;
  precision?: number;
  stepLabel: string;
};

// Keep the draft text (including empty and invalid input) until form validation.
export function NumberInput({value,onChange,unit,precision=0,stepLabel,min,max,...props}: NumberInputProps) {
  const input = useRef<HTMLInputElement>(null);
  // Fluent commits text and steps in one mouse event. Commit the latest draft
  // first so clicking an arrow after typing starts from that draft.
  const commitBeforeStep = () => flushSync(() => input.current?.blur());
  return <div className="number-input"><SpinButton {...props} ref={input} min={min} max={max} step={1} precision={precision}
    value={value.trim() && Number.isFinite(Number(value)) ? Number(value) : null} displayValue={value}
    input={{min,max,step:1,...(props['aria-label'] ? {'aria-labelledby':undefined} : {}),inputMode:precision ? 'decimal' : 'numeric',onChange:e=>onChange(e.target.value)}}
    incrementButton={{'aria-label':'增加'+stepLabel,onMouseDownCapture:commitBeforeStep}}
    decrementButton={{'aria-label':'减少'+stepLabel,onMouseDownCapture:commitBeforeStep}}
    onChange={(_,v)=>onChange(v.displayValue ?? String(v.value ?? ''))}/>
    {unit && <Text className="secondary" aria-hidden="true">{unit}</Text>}
  </div>;
}
