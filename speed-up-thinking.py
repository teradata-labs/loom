#!/usr/bin/env python3
"""
Speed up LLM thinking periods in asciinema recordings.

This script identifies periods where the LLM is executing (minimal output, long pauses)
and speeds them up while keeping interactive typing and rapid output at normal speed.
"""

import json
import sys

def speed_up_recording(input_file, output_file, thinking_speedup=3.0, pause_threshold=0.5, max_pause=1.0):
    """
    Speed up thinking periods in asciinema recording.

    Args:
        input_file: Input .cast file
        output_file: Output .cast file
        thinking_speedup: Speed multiplier for thinking periods (3.0 = 3x faster)
        pause_threshold: Gaps > this (seconds) are considered "thinking" (default: 0.5s)
        max_pause: Maximum pause duration in output (seconds)
    """
    with open(input_file, 'r') as f:
        lines = f.readlines()

    # Parse header
    header = json.loads(lines[0])

    # Parse events (timestamps are RELATIVE to previous event)
    events = [json.loads(line) for line in lines[1:]]

    # Calculate absolute timestamps for analysis
    absolute_time = 0.0
    absolute_timestamps = []
    for timestamp, event_type, data in events:
        absolute_time += timestamp
        absolute_timestamps.append(absolute_time)

    original_duration = absolute_timestamps[-1]
    print(f"📊 Original recording: {len(events)} events, {original_duration:.1f}s ({original_duration/60:.1f} min)")

    # Analyze thinking periods
    thinking_count = 0
    thinking_time = 0.0
    fast_count = 0

    for i, (timestamp, event_type, data) in enumerate(events):
        if timestamp > pause_threshold:
            thinking_count += 1
            thinking_time += timestamp
            if timestamp > 10:
                print(f"   🧠 Long pause at {absolute_timestamps[i]:.1f}s: {timestamp:.1f}s gap")

    print(f"\n🧠 Detected {thinking_count} pauses > {pause_threshold}s")
    print(f"⏱️  Total thinking time: {thinking_time:.1f}s ({thinking_time/60:.1f} min)")

    # Adjust timestamps
    adjusted_events = []
    time_saved = 0.0

    for i, (timestamp, event_type, data) in enumerate(events):
        # If this is a long pause (thinking), speed it up
        if timestamp > pause_threshold:
            # Speed up the gap
            adjusted_timestamp = timestamp / thinking_speedup
            # But cap at max_pause
            adjusted_timestamp = min(adjusted_timestamp, max_pause)
            time_saved += (timestamp - adjusted_timestamp)
        elif timestamp > max_pause:
            # Cap any pause at max_pause
            adjusted_timestamp = max_pause
            time_saved += (timestamp - max_pause)
        else:
            # Keep short gaps as-is (interactive typing, rapid output)
            adjusted_timestamp = timestamp

        adjusted_events.append([adjusted_timestamp, event_type, data])

    # Calculate new duration
    new_duration = sum(event[0] for event in adjusted_events)

    print(f"\n✅ Results:")
    print(f"   Original duration: {original_duration:.1f}s ({original_duration/60:.1f} min)")
    print(f"   New duration: {new_duration:.1f}s ({new_duration/60:.1f} min)")
    print(f"   Time saved: {time_saved:.1f}s ({time_saved/60:.1f} min)")
    print(f"   Speedup: {original_duration/new_duration:.2f}x overall")

    # Write output
    with open(output_file, 'w') as f:
        f.write(json.dumps(header) + '\n')
        for event in adjusted_events:
            f.write(json.dumps(event) + '\n')

    print(f"\n💾 Saved to: {output_file}")
    print(f"\n🎬 Next steps:")
    print(f"   agg --fps-cap 15 --theme dracula {output_file} output.gif")
    print(f"   OR")
    print(f"   agg --speed 1.3 --fps-cap 15 --theme dracula {output_file} output.gif")

def main():
    if len(sys.argv) < 2:
        print("Usage: ./speed-up-thinking.py input.cast [output.cast] [speedup] [pause_threshold]")
        print()
        print("Examples:")
        print("  ./speed-up-thinking.py loomdemo")
        print("  ./speed-up-thinking.py loomdemo loomdemo-fast")
        print("  ./speed-up-thinking.py loomdemo loomdemo-fast 5.0")
        print("  ./speed-up-thinking.py loomdemo loomdemo-fast 5.0 1.0")
        print()
        print("Parameters:")
        print("  speedup: Speed multiplier for pauses (default: 3.0)")
        print("  pause_threshold: Gaps > this are sped up (default: 0.5s)")
        print()
        print("What it does:")
        print("  - Pauses > 0.5s are sped up by 3x (thinking periods)")
        print("  - Pauses > 1.0s are capped at 1.0s max")
        print("  - Short gaps (typing, output) kept at normal speed")
        sys.exit(1)

    input_file = sys.argv[1]

    # Add .cast extension if missing
    if not input_file.endswith('.cast') and '.' not in input_file:
        test_path = input_file + '.cast'
        try:
            with open(test_path, 'r'):
                input_file = test_path
        except:
            pass  # Use original name

    output_file = sys.argv[2] if len(sys.argv) > 2 else input_file.replace('.cast', '-fast').replace('loomdemo', 'loomdemo-fast')
    if not output_file.endswith('.cast') and output_file != input_file:
        output_file += '.cast'

    speedup = float(sys.argv[3]) if len(sys.argv) > 3 else 3.0
    pause_threshold = float(sys.argv[4]) if len(sys.argv) > 4 else 0.5

    print(f"🚀 Speeding up thinking periods...")
    print(f"   Speedup: {speedup}x for pauses > {pause_threshold}s")
    print(f"   Max pause in output: 1.0s")
    print()

    speed_up_recording(input_file, output_file, thinking_speedup=speedup, pause_threshold=pause_threshold, max_pause=1.0)

if __name__ == '__main__':
    main()
