Bevcheck is a self-contained prototype which allows a user (via browser) to automate part of an Alcohol and Tobacco Tax and Trade Bureau (TTB) Certificate of Label Approval (COLA) review of an alcoholic beverage.

--------

Bevcheck's verification is deterministic, which means it gives you the same results for the same inputs every time.

Those inputs are (1) one or more JPEG/PNG images of the beverage's label artwork (with appropriate text legibly-displayed) & (2) the same number of forms, each consisting of a certain subset of the fields of a TTB Form 5100.31.

(Such images & form-fields may be submitted one-by-one through Bevcheck's accessible web-client -- but technically-skilled users may prefer to batch hundreds of applications for simultaneous verification, by first converting their form-data into JSON objects. Refer to the "batch processing" dropdown menu in Bevcheck's web-client.)

Label text is extracted by a (deterministic) OCR system. The mandatory fields are identified & inspected for compliance with Parts 4, 5, 7, & 16 of Title 27 of the Code of Federal Regulations -- and Bevcheck's inspection is tolerant of formatting differences, flagging only cases of substantial textual deviation. (Except in those cases where text-formatting itself is specified by regulation, namely the capitalization of "GOVERNMENT WARNING" on beverage labels.)

(Checked: brand name, class/type designation, alcohol content (ABV + proof cross-check), net contents, name & address (producer / bottler / importer), country of origin (imports), the mandatory government health warning (presence, wording, caps), and the conditional disclosures -- sulfites, aspartame, FD&C Yellow No. 5, cochineal/carmine, coloring, neutral spirits, age, state of distillation, and malt low/non-alcoholic designations.)



HOW TO SET UP & DEPLOY BEVCHECK
===============================

You need to have
(1) an installation of Go 1.26+
(2) an AWS account.

Download a local copy of this project, navigate to its root directory, and run:
go run ./cmd/deploy

That's it! The command will print out an IPv4 address that you can directly paste in your browser for internal use / testing.

(Bevcheck already has rate-limiting, but one would want to add an authentication system for production use. One would NOT need to scale horizontally; even under pessimistic assumptions, Bevcheck's single-server structure would suffice to handle a thousandfold more annual applications than the TTB currently processes.)

On AWS credentials: Bevcheck looks in the usual places. First, the environment-variables AWS_ACCESS_KEY_ID & AWS_SECRET_ACCESS_KEY (AWS_SESSION_TOKEN, if using temporary credentials), then secondly the paths "~/.aws/credentials" and "~/.aws/config". Bevcheck will deploy to the AWS region you've set, defaulting to North Virginia if you haven't. 

TEARDOWN
--------

As above, but:
go run ./cmd/deploy down



XXXX
====





































