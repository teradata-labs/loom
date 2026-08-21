#!/usr/bin/env python3
"""
LongMemEval QA evaluation script.
Adapted from https://github.com/xiaowu0162/LongMemEval/blob/main/src/evaluation/evaluate_qa.py

Changes from upstream:
  - Added gpt-5.1 to model_zoo (gpt-4o retired Feb 2026)
  - Default metric model changed to gpt-5.1
  - Judge reply budget is 64 tokens (upstream: max_tokens=10). With the
    upstream substring check ('yes' in response) a verbose judge has more
    room to emit an incidental "yes"; disclosed here, kept for parity with
    published runs.
  - Added 'bedrock' backend (boto3 converse; credentials AND region from the
    AWS default chain — AWS_REGION/AWS_DEFAULT_REGION/profile — with
    us-west-2 only as the last-resort default) and 'azure' backend
    (AzureOpenAI; needs AZURE_OPENAI_ENDPOINT, AZURE_OPENAI_API_KEY, and
    AZURE_OPENAI_DEPLOYMENT_ID). Judge prompts are unchanged from the paper.
"""

import os
import sys
import json
from tqdm import tqdm
import backoff
import openai
from openai import OpenAI
import numpy as np


model_zoo = {
    'llama-3.1-70b-instruct': ('meta-llama/Meta-Llama-3.1-70B-Instruct', 'local'),
    'gpt-4o-mini': ('gpt-4o-mini-2024-07-18', 'openai'),
    'gpt-4o': ('gpt-4o-2024-08-06', 'openai'),
    'gpt-5.1': ('gpt-5.1', 'openai'),
    'claude-opus-4-6-bedrock': ('global.anthropic.claude-opus-4-6-v1', 'bedrock'),
    'azure-gpt': (os.getenv('AZURE_OPENAI_DEPLOYMENT_ID', ''), 'azure'),
}


def bedrock_converse_with_backoff(client, model_id, prompt, max_tries=5):
    import random
    import time
    from botocore.exceptions import ClientError
    retryable = ('ThrottlingException', 'ServiceUnavailableException', 'ModelNotReadyException')
    for attempt in range(max_tries):
        try:
            resp = client.converse(
                modelId=model_id,
                messages=[{'role': 'user', 'content': [{'text': prompt}]}],
                inferenceConfig={'maxTokens': 64, 'temperature': 0},
            )
            return resp['output']['message']['content'][0]['text']
        except ClientError as e:
            code = e.response.get('Error', {}).get('Code', '')
            if code in retryable and attempt < max_tries - 1:
                time.sleep(min(2 ** attempt + random.random(), 30))
                continue
            raise
    raise RuntimeError('bedrock converse retries exhausted')


@backoff.on_exception(backoff.expo, (openai.RateLimitError,
                                    openai.APIError),
                      giveup=lambda e: isinstance(e, openai.AuthenticationError),
                      max_tries=5)
def chat_completions_with_backoff(client, **kwargs):
    return client.chat.completions.create(**kwargs)


def get_anscheck_prompt(task, question, answer, response, abstention=False):
    if not abstention:
        if task in ['single-session-user', 'single-session-assistant', 'multi-session']:
            template = "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no. If the response is equivalent to the correct answer or contains all the intermediate steps to get the correct answer, you should also answer yes. If the response only contains a subset of the information required by the answer, answer no. \n\nQuestion: {}\n\nCorrect Answer: {}\n\nModel Response: {}\n\nIs the model response correct? Answer yes or no only."
            prompt = template.format(question, answer, response)
        elif task == 'temporal-reasoning':
            template = "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no. If the response is equivalent to the correct answer or contains all the intermediate steps to get the correct answer, you should also answer yes. If the response only contains a subset of the information required by the answer, answer no. In addition, do not penalize off-by-one errors for the number of days. If the question asks for the number of days/weeks/months, etc., and the model makes off-by-one errors (e.g., predicting 19 days when the answer is 18), the model's response is still correct. \n\nQuestion: {}\n\nCorrect Answer: {}\n\nModel Response: {}\n\nIs the model response correct? Answer yes or no only."
            prompt = template.format(question, answer, response)
        elif task == 'knowledge-update':
            template = "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no. If the response contains some previous information along with an updated answer, the response should be considered as correct as long as the updated answer is the required answer.\n\nQuestion: {}\n\nCorrect Answer: {}\n\nModel Response: {}\n\nIs the model response correct? Answer yes or no only."
            prompt = template.format(question, answer, response)
        elif task == 'single-session-preference':
            template = "I will give you a question, a rubric for desired personalized response, and a response from a model. Please answer yes if the response satisfies the desired response. Otherwise, answer no. The model does not need to reflect all the points in the rubric. The response is correct as long as it recalls and utilizes the user's personal information correctly.\n\nQuestion: {}\n\nRubric: {}\n\nModel Response: {}\n\nIs the model response correct? Answer yes or no only."
            prompt = template.format(question, answer, response)
        else:
            raise NotImplementedError
    else:
        template = "I will give you an unanswerable question, an explanation, and a response from a model. Please answer yes if the model correctly identifies the question as unanswerable. The model could say that the information is incomplete, or some other information is given but the asked information is not.\n\nQuestion: {}\n\nExplanation: {}\n\nModel Response: {}\n\nDoes the model correctly identify the question as unanswerable? Answer yes or no only."
        prompt = template.format(question, answer, response)
    return prompt


if __name__ == '__main__':
    if len(sys.argv) < 3 or len(sys.argv) > 4:
        print('Usage: python evaluate_qa.py hyp_file ref_file [metric_model]')
        print('  metric_model defaults to gpt-5.1')
        print('  available models:', ', '.join(model_zoo.keys()))
        exit()

    hyp_file = sys.argv[1]
    ref_file = sys.argv[2]
    metric_model_short = sys.argv[3] if len(sys.argv) == 4 else 'gpt-5.1'
    verbose = True

    result_file = hyp_file + '.eval-results-{}'.format(metric_model_short)

    if metric_model_short not in model_zoo:
        print('Requested metric model is not supported:', metric_model_short)
        print('Available:', ', '.join(model_zoo.keys()))
        exit()
    metric_model, metric_model_source = model_zoo[metric_model_short]
    metric_client = None
    bedrock_client = None
    if metric_model_source == 'openai':
        openai.organization = os.getenv('OPENAI_ORGANIZATION')
        metric_client = OpenAI(api_key=os.getenv('OPENAI_API_KEY'))
    elif metric_model_source == 'azure':
        from openai import AzureOpenAI
        endpoint = os.getenv('AZURE_OPENAI_ENDPOINT')
        if not endpoint or not metric_model:
            print('azure backend requires AZURE_OPENAI_ENDPOINT and AZURE_OPENAI_DEPLOYMENT_ID')
            exit(1)
        metric_client = AzureOpenAI(
            azure_endpoint=endpoint,
            api_key=os.getenv('AZURE_OPENAI_API_KEY'),
            api_version=os.getenv('AZURE_OPENAI_API_VERSION', '2024-10-21'),
        )
    elif metric_model_source == 'bedrock':
        import boto3
        bedrock_client = boto3.client(
            'bedrock-runtime',
            # Respect the full default chain (env, profile, SSO); only fall
            # back to us-west-2 when nothing in the chain names a region.
            region_name=os.getenv('AWS_REGION') or os.getenv('AWS_DEFAULT_REGION') or boto3.session.Session().region_name or 'us-west-2',
        )
    else:
        metric_client = OpenAI(api_key="EMPTY", base_url="http://localhost:8001/v1")

    try:
        hypotheses = [json.loads(line) for line in open(hyp_file).readlines()]
    except:
        hypotheses = json.load(open(hyp_file))
    try:
        references = json.load(open(ref_file))
    except:
        references = [json.loads(line) for line in open(ref_file).readlines()]
    qid2qdata = {entry['question_id']: entry for entry in references}
    qid2qtype = {entry['question_id']: entry['question_type'] for entry in references}
    qtypes = set(list(qid2qtype.values()))
    qtype2acc = {t: [] for t in qtypes}

    with open(result_file, 'w') as out_f:
        logs = []
        for entry in tqdm(hypotheses):

            if entry['question_id'] not in qid2qtype:
                print('Warning: skipping {} as it is not in reference data.'.format(entry['question_id']))
                continue

            qtype = qid2qtype[entry['question_id']]
            q = qid2qdata[entry['question_id']]['question']
            ans = qid2qdata[entry['question_id']]['answer']
            hyp = entry['hypothesis']

            prompt = get_anscheck_prompt(qtype, q, ans, hyp, abstention='_abs' in entry['question_id'])
            if metric_model_source == 'bedrock':
                eval_response = bedrock_converse_with_backoff(bedrock_client, metric_model, prompt).strip()
            else:
                kwargs = {
                    'model': metric_model,
                    'messages': [
                        {"role": "user", "content": prompt}
                    ],
                    'n': 1,
                    'temperature': 0,
                    'max_completion_tokens': 64
                }
                completion = chat_completions_with_backoff(metric_client, **kwargs)
                eval_response = completion.choices[0].message.content.strip()
            label = 'yes' in eval_response.lower()
            entry['autoeval_label'] = {
                'model': metric_model,
                'label': label
            }
            logs.append(entry)
            if verbose:
                print(json.dumps({
                    'question': q,
                    'answer': ans,
                    'hypothesis': hyp,
                    'autoeval_label': label
                }, indent=4), flush=True)
            print(json.dumps(entry), file=out_f)
            qtype2acc[qid2qtype[entry['question_id']]].append(1 if label else 0)


    print('Accuracy:', round(np.mean([1 if x['autoeval_label']['label'] else 0 for x in logs]).item(), 4))
    for k, v in qtype2acc.items():
        print('\t{}: {} ({})'.format(k, round(np.mean(v), 4), len(v)))

    print('Saved to', result_file)
