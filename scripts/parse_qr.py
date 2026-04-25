import base64, sys

data = base64.b64decode('AW/YtNix2YPYqSDYqtmI2LHZitivINin2YTYqtmD2YbZiNmE2YjYrNmK2Kcg2KjYo9mC2LXZiSDYs9ix2LnYqSDYp9mE2YXYrdiv2YjYr9ipIHwgTWF4aW11bSBTcGVlZCBUZWNoIFN1cHBseSBMVEQCDzM5OTk5OTk5OTkwMDAwMwMTMjAyMi0wOS0wN1QxMjoyMToyOAQENC42MAUDMC42BixmKzBXQ3FuUGtJbkkrZUw5RzNMQXJ5MTJmVFBmK3RvQzlVWDA3RjRmSStzPQdgTUVVQ0lCeHlSOHJjNEs4NzI4d2RTRjRYU0RxUHMrcklMKzNURmg5bSthTnhRUHRTQWlFQTZjSGFwSXR2cDEzeU1TdTY2TmJPZzJDcG9tSHdVU25ZSjloNnVHUTY1YVk9CFgwVjAQBgcqhkjOPQIBBgUrgQQACgNCAAShYIprRJr0UgStM6/S4CQLVUgpfFT2c+nHa+V/jKEx6PLxzTZcluUOru0/J2jyarRqE4yY2jyDCeLte3UpP1R4')

# Also parse simplified QR
data2 = base64.b64decode('AW/YtNix2YPYqSDYqtmI2LHZitivINin2YTYqtmD2YbZiNmE2YjYrNmK2Kcg2KjYo9mC2LXZiSDYs9ix2LnYqSDYp9mE2YXYrdiv2YjYr9ipIHwgTWF4aW11bSBTcGVlZCBUZWNoIFN1cHBseSBMVEQCDzM5OTk5OTk5OTkwMDAwMwMTMjAyMi0wOC0xN1QxNzo0MTowOAQGMjMxLjE1BQUzMC4xNQYsSHNzMmdORmpCWTVPSm4vNUNFVlpTU05VTXJTZjRRbENNeHdzaW9QTjZmQT0HYE1FVUNJUUNzK0ROUSSF2bHo3Sm9vdkE3SlJqYWtuNHRVczBKbENjQW9KTmgvSjY1Rkh3SWdLcHB0MitEZmNMWHRLUTZ5UjQ5dGNWeWRncy9NU1kyeVY5dkFUemNwVXE0PQhYMFYwEAYHKoZIzj0CAQYFK4EEAAoDQgAEoWCKa0Sa9FIErTOv0uAkC1VIKXxU9nPpx2vlf4yhMejy8c02XJblDq7tPydo8mq0ahOMmNo8gwni7Xt1KT9UeAlHMEUCIQCxP4nIZp1lwlClG3Gt8nIvKKsGi7xXR1Y0K73iPbqgGwIgPYQuDPI4DAQAz0s5ndrojyQOoCkdyxNN1O+Xqmwv61w=')

for label, d in [("Standard QR", data), ("Simplified QR", data2)]:
    print(f"\n=== {label} ===")
    i = 0
    while i < len(d):
        tag = d[i]; i += 1
        length = d[i]; i += 1
        value = d[i:i+length]; i += length
        try:
            txt = value.decode('utf-8')
        except:
            txt = f"[binary {len(value)} bytes] base64={base64.b64encode(value).decode()}"
        print(f"  Tag {tag} (len={length}): {txt}")
